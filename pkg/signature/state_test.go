/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package signature

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const (
	testManifestSHA = "1111111111111111111111111111111111111111111111111111111111111111"
	testImageDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testLockSHA     = "3333333333333333333333333333333333333333333333333333333333333333"
)

func validState() State {
	return State{
		ABI:            ABIVersion,
		Origin:         "github.com/acme/cpak",
		ManifestSHA256: testManifestSHA,
		ImageDigest:    testImageDigest,
		LockSHA256:     testLockSHA,
		Generation:     7,
	}
}

func TestCanonicalIsTheDocumentedByteString(t *testing.T) {
	want := "cpak.signature.state.v1\n" +
		"abi=1\n" +
		"origin=github.com/acme/cpak\n" +
		"manifest_sha256=" + testManifestSHA + "\n" +
		"image_digest=" + testImageDigest + "\n" +
		"lock_sha256=" + testLockSHA + "\n" +
		"generation=7\n"

	canonical, err := validState().Canonical()
	if err != nil {
		t.Fatalf("a valid state must have a canonical encoding: %v", err)
	}
	if string(canonical) != want {
		t.Fatalf("the canonical encoding is the signed message and must be exactly the documented bytes\n got: %q\nwant: %q", canonical, want)
	}
}

// The lock line is written even when there is no lock, so that a state with a
// lock and a state without one can never produce the same bytes.
func TestCanonicalKeepsTheLockLineWhenThereIsNoLock(t *testing.T) {
	state := validState()
	state.LockSHA256 = ""

	canonical, err := state.Canonical()
	if err != nil {
		t.Fatalf("a state without a lock is valid: %v", err)
	}
	if !strings.Contains(string(canonical), "\nlock_sha256=\n") {
		t.Fatalf("an absent lock must still be a line, otherwise two states share one encoding: %q", canonical)
	}
}

func TestCanonicalIsStable(t *testing.T) {
	first, err := validState().Canonical()
	if err != nil {
		t.Fatalf("canonical encoding failed: %v", err)
	}
	for i := 0; i < 64; i++ {
		again, err := validState().Canonical()
		if err != nil {
			t.Fatalf("canonical encoding failed on pass %d: %v", i, err)
		}
		if string(again) != string(first) {
			t.Fatalf("the same state must encode the same way every time, pass %d gave %q", i, again)
		}
	}
}

// Every field has to reach the signed bytes. A field that did not would be a
// field a publisher could change without breaking a signature.
func TestEveryFieldChangesTheDigest(t *testing.T) {
	base, err := validState().Digest()
	if err != nil {
		t.Fatalf("digest of a valid state failed: %v", err)
	}
	changes := map[string]State{
		"origin":     {ABI: ABIVersion, Origin: "github.com/acme/other", ManifestSHA256: testManifestSHA, ImageDigest: testImageDigest, LockSHA256: testLockSHA, Generation: 7},
		"manifest":   {ABI: ABIVersion, Origin: "github.com/acme/cpak", ManifestSHA256: strings.Repeat("a", 64), ImageDigest: testImageDigest, LockSHA256: testLockSHA, Generation: 7},
		"image":      {ABI: ABIVersion, Origin: "github.com/acme/cpak", ManifestSHA256: testManifestSHA, ImageDigest: "sha256:" + strings.Repeat("b", 64), LockSHA256: testLockSHA, Generation: 7},
		"lock":       {ABI: ABIVersion, Origin: "github.com/acme/cpak", ManifestSHA256: testManifestSHA, ImageDigest: testImageDigest, LockSHA256: strings.Repeat("c", 64), Generation: 7},
		"generation": {ABI: ABIVersion, Origin: "github.com/acme/cpak", ManifestSHA256: testManifestSHA, ImageDigest: testImageDigest, LockSHA256: testLockSHA, Generation: 8},
	}
	for field, changed := range changes {
		digest, err := changed.Digest()
		if err != nil {
			t.Fatalf("digest with a changed %s failed: %v", field, err)
		}
		if digest == base {
			t.Fatalf("changing the %s left the digest alone, so that field is not signed", field)
		}
	}
}

func TestDigestIsTheHashOfTheCanonicalEncoding(t *testing.T) {
	state := validState()
	canonical, err := state.Canonical()
	if err != nil {
		t.Fatalf("canonical encoding failed: %v", err)
	}
	sum := sha256.Sum256(canonical)

	digest, err := state.Digest()
	if err != nil {
		t.Fatalf("digest failed: %v", err)
	}
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("the digest must be the sha256 of the canonical bytes, got %s", digest)
	}
}

func TestValidateRefusesAStateThatCannotBeMeaningful(t *testing.T) {
	cases := map[string]func(*State){
		"an abi from another set of rules":     func(s *State) { s.ABI = ABIVersion + 1 },
		"an unset abi":                         func(s *State) { s.ABI = 0 },
		"an empty origin":                      func(s *State) { s.Origin = "" },
		"an origin with no host":               func(s *State) { s.Origin = "acme/cpak" },
		"an origin with a host that is a word": func(s *State) { s.Origin = "github/acme/cpak" },
		"an origin with a fourth part":         func(s *State) { s.Origin = "github.com/acme/cpak/extra" },
		"an origin that is not lowercase":      func(s *State) { s.Origin = "github.com/Acme/cpak" },
		"an origin with a traversal":           func(s *State) { s.Origin = "github.com/acme/.." },
		"an origin with a space":               func(s *State) { s.Origin = "github.com/acme/cp ak" },
		"an origin with a newline":             func(s *State) { s.Origin = "github.com/acme/cpak\nabi=2" },
		"a manifest hash that is not hex":      func(s *State) { s.ManifestSHA256 = strings.Repeat("z", 64) },
		"a manifest hash of the wrong length":  func(s *State) { s.ManifestSHA256 = "abcd" },
		"an uppercase manifest hash":           func(s *State) { s.ManifestSHA256 = strings.Repeat("A", 64) },
		"a missing manifest hash":              func(s *State) { s.ManifestSHA256 = "" },
		"an image reference that is a tag":     func(s *State) { s.ImageDigest = "ghcr.io/acme/cpak:main" },
		"an image digest with no algorithm":    func(s *State) { s.ImageDigest = testManifestSHA },
		"an image digest of another algorithm": func(s *State) { s.ImageDigest = "sha512:" + strings.Repeat("a", 64) },
		"a missing image digest":               func(s *State) { s.ImageDigest = "" },
		"a lock hash that is not a sha256":     func(s *State) { s.LockSHA256 = "nope" },
		"a generation of zero":                 func(s *State) { s.Generation = 0 },
	}
	for name, spoil := range cases {
		state := validState()
		spoil(&state)
		err := state.Validate()
		if err == nil {
			t.Fatalf("Validate accepted %s", name)
		}
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Validate rejected %s with an error a caller cannot recognise: %v", name, err)
		}
		if _, err = state.Canonical(); err == nil {
			t.Fatalf("Canonical produced signable bytes for %s", name)
		}
		if _, err = state.Digest(); err == nil {
			t.Fatalf("Digest produced a signable digest for %s", name)
		}
	}
}

func TestValidateAcceptsAStateWithoutALock(t *testing.T) {
	state := validState()
	state.LockSHA256 = ""
	if err := state.Validate(); err != nil {
		t.Fatalf("a package with no lock file must still be signable: %v", err)
	}
}
