/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// These run the real spawning machinery and look at what it does to the
// process on the far side. Everything the split promises is a property of that
// process, so a test that only exercises the protocol proves none of it.
//
// The child role is played by this test binary re-entered in helper mode, put
// there through the same variable the authority uses to address itself. What
// is under test is the machinery, not the helper.

const helperMarker = "cpak-verifier-helper"

func useHelperChild(t *testing.T, mode string) {
	t.Helper()
	saved := verifierArgv
	t.Cleanup(func() { verifierArgv = saved })
	verifierArgv = func() []string {
		return []string{"-test.run=TestVerifierHelperProcess", "--", helperMarker, mode}
	}
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("dropping privileges needs privileges to drop")
	}
}

func helperMode() (string, bool) {
	for index, argument := range os.Args {
		if argument == helperMarker && index+1 < len(os.Args) {
			return os.Args[index+1], true
		}
	}
	return "", false
}

// TestVerifierHelperProcess is not a test. It is the process the cases above
// put on the far side of the spawn, and it exits before the testing package
// can write anything of its own onto the pipe the parent is reading.
func TestVerifierHelperProcess(t *testing.T) {
	mode, wanted := helperMode()
	if !wanted {
		t.Skip("not the helper")
	}
	switch mode {
	case "credentials":
		report(fmt.Sprintf("uid=%d euid=%d gid=%d", os.Getuid(), os.Geteuid(), os.Getgid()))
	case "interfaces":
		names := []string{}
		interfaces, err := net.Interfaces()
		if err != nil {
			report("interfaces failed: " + err.Error())
		}
		for _, held := range interfaces {
			names = append(names, held.Name)
		}
		report("interfaces=" + strings.Join(names, ","))
	case "silent":
		time.Sleep(time.Minute)
	case "flood":
		report(strings.Repeat("a", verifierResponseLimit+1))
	case "garbage":
		fmt.Fprintln(os.Stdout, "this is not a response")
	}
	os.Exit(0)
}

func report(reason string) {
	_ = json.NewEncoder(os.Stdout).Encode(verifierResponse{Error: reason})
	os.Exit(0)
}

// The property the whole split exists for. If the child keeps root, nothing
// else in this file matters.
func TestTheChildDoesNotKeepRoot(t *testing.T) {
	requireRoot(t)
	useHelperChild(t, "credentials")
	_, err := separatedVerify([]byte("bundle"), probeState())
	if err == nil {
		t.Fatal("the helper reported nothing")
	}
	reported := err.Error()
	if !strings.Contains(reported, "uid=") {
		t.Fatalf("the helper did not report its credentials: %s", reported)
	}
	for _, forbidden := range []string{"uid=0", "euid=0", "gid=0"} {
		if strings.Contains(reported, forbidden) {
			t.Fatalf("the child kept root: %s", reported)
		}
	}
	if !strings.Contains(reported, fmt.Sprintf("uid=%d", unprivilegedUID)) {
		t.Fatalf("the child did not land on the identity it was given: %s", reported)
	}
}

// The check is offline by contract. Taking the network away makes that a fact
// about the process instead of a promise about the code.
func TestTheChildHasNoNetwork(t *testing.T) {
	requireRoot(t)
	useHelperChild(t, "interfaces")
	_, err := separatedVerify([]byte("bundle"), probeState())
	if err == nil {
		t.Fatal("the helper reported nothing")
	}
	reported := err.Error()
	if !strings.Contains(reported, "interfaces=") {
		t.Fatalf("the helper did not report its interfaces: %s", reported)
	}
	// A fresh network namespace holds loopback and nothing else, so anything
	// beyond it means the child is still on the host's network.
	listed := strings.TrimPrefix(reported[strings.Index(reported, "interfaces="):], "interfaces=")
	if listed != "lo" {
		t.Fatalf("the child can still see %s", listed)
	}
}

// A child that never answers must not hold the authority open behind it.
func TestAChildThatNeverAnswersIsGivenUpOn(t *testing.T) {
	requireRoot(t)
	useHelperChild(t, "silent")
	saved := verifierTimeout
	t.Cleanup(func() { verifierTimeout = saved })
	verifierTimeout = 2 * time.Second

	start := time.Now()
	_, err := separatedVerify([]byte("bundle"), probeState())
	if err == nil {
		t.Fatal("a child that answered nothing was read as a verdict")
	}
	if !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("the wait ended as something other than an unavailable verifier: %v", err)
	}
	if waited := time.Since(start); waited > 20*time.Second {
		t.Fatalf("the authority waited %s on a child it had bounded to %s", waited, verifierTimeout)
	}
}

// The answer is the one thing the parent reads back, so a child that floods it
// or writes something else must not be able to make the parent chew on it.
func TestAnAnswerTheParentCannotNameIsRefused(t *testing.T) {
	requireRoot(t)
	for name, mode := range map[string]string{
		"an answer past the limit":  "flood",
		"an answer that is not one": "garbage",
	} {
		useHelperChild(t, mode)
		if _, err := separatedVerify([]byte("bundle"), probeState()); err == nil {
			t.Fatalf("%s was accepted", name)
		} else if !errors.Is(err, ErrVerifierUnavailable) {
			t.Fatalf("%s ended as something other than an unavailable verifier: %v", name, err)
		}
	}
}

// The real thing, with no helper in the way: the authority as it ships, given a
// bundle nobody signed, must refuse it from inside the child and must say why
// rather than reporting that the verifier would not start.
//
// It can only run where the executable is cpak. A test binary asked to be the
// child would read the argument as its own flag, so the case skips rather than
// reporting something about a program that is not the one under test.
func TestTheShippedVerifierRefusesABundleNobodySigned(t *testing.T) {
	requireRoot(t)
	self, err := os.Executable()
	if err != nil || strings.HasSuffix(self, ".test") {
		t.Skip("the child role needs the cpak binary, not a test binary")
	}
	if _, err := separatedVerify([]byte(`{"not":"a bundle"}`), probeState()); err == nil {
		t.Fatal("a bundle nobody signed was accepted")
	} else if errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("the child never ran, so this proves nothing about the check: %v", err)
	}
}
