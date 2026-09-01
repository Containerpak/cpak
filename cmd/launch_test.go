/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/sandbox"
)

func TestLaunchGrantsKeepWriteFilesAccess(t *testing.T) {
	command := LaunchCmd{
		LandlockReadOnly:   []string{"/"},
		LandlockWriteFiles: []string{"/proc"},
		LandlockReadWrite:  []string{"/tmp"},
	}
	want := []sandbox.PathGrant{
		{Path: "/", ReadOnly: true},
		{Path: "/proc", ReadOnly: true, WriteFiles: true},
		{Path: "/tmp"},
	}
	if got := command.landlockGrants(); !reflect.DeepEqual(got, want) {
		t.Fatalf("landlock grants: got %+v, want %+v", got, want)
	}
}

func TestNestedMountsDisableLandlock(t *testing.T) {
	if !(&LaunchCmd{}).useLandlock() {
		t.Fatal("ordinary launch disabled Landlock")
	}
	if (&LaunchCmd{UserNamespaces: true}).useLandlock() {
		t.Fatal("nested sandbox launch kept Landlock enabled")
	}
}

// captureNotices reads what an operator would have read.
func captureNotices(t *testing.T) *bytes.Buffer {
	t.Helper()
	captured := &bytes.Buffer{}
	previous := sandboxNoticeWriter
	sandboxNoticeWriter = captured
	t.Cleanup(func() { sandboxNoticeWriter = previous })
	return captured
}

// An application starts on a kernel that has no Landlock. It always did, and
// the decision is that it always will: what changes is that the operator is
// told, at the launch, in words that say which protection is gone and what it
// was holding back, rather than by one line naming a kernel feature.
func TestAnApplicationStartsAndIsToldWhatItLost(t *testing.T) {
	captured := captureNotices(t)
	grants := []sandbox.PathGrant{
		{Path: "/", ReadOnly: true},
		{Path: "/home/user/Documents"},
	}
	command := &LaunchCmd{}

	if err := command.sandboxOutcome(sandbox.ErrUnavailable, landlockUnavailable(grants)); err != nil {
		t.Fatalf("a kernel without Landlock stopped the application from starting: %v", err)
	}

	notice := captured.String()
	for _, expected := range []string{
		// what is gone, without needing to know the name of it
		"running WITHOUT the file restrictions",
		"started anyway",
		// what it would have stopped
		"would have refused",
		"every write to a path the application was not given",
		// which of this application's own permissions are unenforced
		"/home/user/Documents",
		// what is left, so the reader is not told more than the truth
		"only the files cpak mounted into its container",
		// what to do about it
		"cat /sys/kernel/security/lsm",
		"lsm=",
	} {
		if !strings.Contains(notice, expected) {
			t.Fatalf("the operator is never told %q:\n%s", expected, notice)
		}
	}
	if lines := strings.Count(notice, "\n"); lines < 20 {
		t.Fatalf("a loss this size was reported in %d lines:\n%s", lines, notice)
	}
}

// The other barrier is announced the same way and says its own thing: what an
// application may ask the kernel for is not what it may open.
func TestTheSyscallFilterIsAnnouncedInItsOwnWords(t *testing.T) {
	captured := captureNotices(t)
	if err := (&LaunchCmd{}).sandboxOutcome(sandbox.ErrUnavailable, seccompUnavailable()); err != nil {
		t.Fatalf("a kernel without seccomp stopped the application from starting: %v", err)
	}
	notice := captured.String()
	for _, expected := range []string{
		"running WITHOUT the system call filter",
		"started anyway",
		"tracing other running programs",
		"building namespaces of its own",
		"CONFIG_SECCOMP_FILTER",
	} {
		if !strings.Contains(notice, expected) {
			t.Fatalf("the operator is never told %q:\n%s", expected, notice)
		}
	}
}

// A failure that is not the kernel declining the feature is a failure. Warning
// it away would hide a sandbox that could have been established and was not.
func TestARealSandboxFailureIsNotWarnedAway(t *testing.T) {
	captured := captureNotices(t)
	refused := errors.New("install seccomp filter: operation not permitted")
	err := (&LaunchCmd{}).sandboxOutcome(refused, seccompUnavailable())
	if !errors.Is(err, refused) {
		t.Fatalf("outcome: got %v, want %v", err, refused)
	}
	if captured.Len() != 0 {
		t.Fatalf("a failure was reported as a missing feature:\n%s", captured.String())
	}
}

// cpak's own launches are the one caller that asks to be refused. Nothing of a
// user's is lost when cpak declines to run its own storage helper unrestricted,
// and the refusal has to say what is missing and how to get it.
func TestCpakOwnLaunchesAreStillRefusedWithoutTheSandbox(t *testing.T) {
	captured := captureNotices(t)
	err := (&LaunchCmd{RequireSandbox: true}).sandboxOutcome(sandbox.ErrUnavailable, landlockUnavailable(nil))
	if err == nil {
		t.Fatal("a launch that requires the sandbox ran without it")
	}
	for _, expected := range []string{"Landlock", "cpak's own launches", "lsm="} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("the refusal does not say %q: %s", expected, err)
		}
	}
	if captured.Len() != 0 {
		t.Fatalf("a refused launch also printed the notice for a launch that ran:\n%s", captured.String())
	}
}

// The paths an application may write are what a reader can weigh, so they are
// named. The read-only ones are counted: an ordinary container grants dozens.
func TestTheNoticeNamesWhatTheApplicationMayWrite(t *testing.T) {
	lines := strings.Join(writableGrantLines([]sandbox.PathGrant{
		{Path: "/", ReadOnly: true},
		{Path: "/usr/share/fonts", ReadOnly: true},
		{Path: "/srv/data"},
		{Path: "/etc/hosts", ReadOnly: true, WriteFiles: true},
	}), "\n")
	for _, expected := range []string{"/srv/data", "/etc/hosts"} {
		if !strings.Contains(lines, expected) {
			t.Fatalf("a writable grant is not named: %q\n%s", expected, lines)
		}
	}
	if strings.Contains(lines, "/usr/share/fonts") {
		t.Fatalf("a read-only grant was listed as writable:\n%s", lines)
	}

	nothing := strings.Join(writableGrantLines([]sandbox.PathGrant{{Path: "/", ReadOnly: true}}), "\n")
	if !strings.Contains(nothing, "nothing to write outside its own container") {
		t.Fatalf("an application with no writable grant is described wrongly:\n%s", nothing)
	}

	many := []sandbox.PathGrant{}
	for index := 0; index < listedGrants+5; index++ {
		many = append(many, sandbox.PathGrant{Path: "/srv/data"})
	}
	if capped := strings.Join(writableGrantLines(many), "\n"); !strings.Contains(capped, "and 5 more") {
		t.Fatalf("a long list is not capped:\n%s", capped)
	}
}

// The notice belongs to cpak, so it goes to the error stream. The output stream
// is the one the program about to be executed writes on, and a caller reading
// that output is entitled to find nothing of cpak's in it.
func TestTheNoticeIsWrittenToTheErrorStream(t *testing.T) {
	if sandboxNoticeWriter != io.Writer(os.Stderr) {
		t.Fatalf("notices go to %T instead of the error stream", sandboxNoticeWriter)
	}
	captured := captureNotices(t)
	seccompUnavailable().report(captured)
	if !strings.HasPrefix(captured.String(), "\n"+strings.Repeat("=", sandboxNoticeWidth)) {
		t.Fatalf("the notice is not framed:\n%s", captured.String())
	}
}
