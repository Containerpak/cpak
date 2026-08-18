/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/signature"
)

func probeState() signature.State {
	return signature.State{
		ABI:            1,
		Origin:         "github.com/containerpak/demo",
		ManifestSHA256: strings.Repeat("ab", 32),
		ImageDigest:    "sha256:" + strings.Repeat("cd", 32),
		Generation:     3,
	}
}

func useDirectVerifier(t *testing.T, verify func([]byte, signature.State) (signature.Verified, error)) {
	t.Helper()
	saved := verifyDirect
	t.Cleanup(func() { verifyDirect = saved })
	verifyDirect = verify
}

func TestTheChildAnswersWhoSignedWhenTheBundleStands(t *testing.T) {
	state := probeState()
	useDirectVerifier(t, func(bundle []byte, given signature.State) (signature.Verified, error) {
		if string(bundle) != "the bundle" {
			t.Fatalf("the child was given %q", bundle)
		}
		if given != state {
			t.Fatalf("the child was given another state: %+v", given)
		}
		return signature.Verified{State: given, Identity: signature.Identity{
			Issuer: "https://token.actions.githubusercontent.com",
			Repo:   "github.com/containerpak/demo",
		}}, nil
	})

	request, err := json.Marshal(verifierRequest{Bundle: []byte("the bundle"), State: state})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunVerifier(bytes.NewReader(request), &out); err != nil {
		t.Fatal(err)
	}
	var answer verifierResponse
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Error != "" || answer.Verified == nil {
		t.Fatalf("a bundle that stands came back as %+v", answer)
	}
	if answer.Verified.Identity.Repo != "github.com/containerpak/demo" {
		t.Fatalf("the child named another signer: %+v", answer.Verified.Identity)
	}
}

// A bundle that does not stand is the ordinary case, not a broken child, so it
// has to travel back as a reason the parent can report.
func TestTheChildReportsARefusalInsteadOfDying(t *testing.T) {
	useDirectVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		return signature.Verified{}, errors.New("no transparency log holds this")
	})
	request, err := json.Marshal(verifierRequest{Bundle: []byte("x"), State: probeState()})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunVerifier(bytes.NewReader(request), &out); err != nil {
		t.Fatalf("a refused bundle failed the child itself: %v", err)
	}
	var answer verifierResponse
	if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Verified != nil || !strings.Contains(answer.Error, "transparency log") {
		t.Fatalf("the refusal did not come back as a reason: %+v", answer)
	}
}

// The request is the one place the child reads bytes it did not choose, so a
// request it cannot name exactly is refused rather than guessed at.
func TestTheChildRefusesARequestItCannotName(t *testing.T) {
	for name, request := range map[string]string{
		"a field no request has": `{"bundle":"","state":{},"extra":true}`,
		"not an object at all":   `["bundle"]`,
		"nothing":                ``,
	} {
		var out bytes.Buffer
		if err := RunVerifier(strings.NewReader(request), &out); err == nil {
			t.Fatalf("%s was accepted as a request", name)
		}
	}
}

func TestARequestPastTheLimitIsRefused(t *testing.T) {
	var out bytes.Buffer
	oversized := strings.Repeat("a", verifierRequestLimit+1)
	if err := RunVerifier(strings.NewReader(oversized), &out); err == nil {
		t.Fatal("a request past the limit was read")
	}
}

// An authority with no privileges has nothing to separate, and forking there
// would only make the same check slower and harder to test.
func TestAnUnprivilegedAuthorityChecksInPlace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this case is about a process that is not root")
	}
	called := false
	useDirectVerifier(t, func([]byte, signature.State) (signature.Verified, error) {
		called = true
		return signature.Verified{State: probeState()}, nil
	})
	if _, err := separatedVerify([]byte("bundle"), probeState()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("the check never reached pkg/signature")
	}
}

// The verifier failing to run is not a bundle failing to verify, and a caller
// that reads one as the other would enrol on a check that never happened.
func TestAVerifierThatCannotRunIsNotARefusedSignature(t *testing.T) {
	if !errors.Is(ErrVerifierUnavailable, ErrVerifierUnavailable) {
		t.Fatal("the sentinel does not match itself")
	}
	wrapped := errWrap(ErrVerifierUnavailable)
	if !errors.Is(wrapped, ErrVerifierUnavailable) {
		t.Fatal("a wrapped unavailability stops being recognisable")
	}
	if errors.Is(wrapped, ErrUnsigned) {
		t.Fatal("a verifier that could not run reads as a package nobody signed")
	}
}

func errWrap(err error) error {
	return &wrapped{err}
}

type wrapped struct{ err error }

func (w *wrapped) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }
