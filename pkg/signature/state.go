/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package signature is the part of an application's identity a publisher can
// answer for without knowing anything about the machine that installs it: the
// origin it is published under, the manifest it is configured by, the image it
// is made of and the lock it was resolved with. That tuple is what is signed,
// and this package is where it is written down, hashed and checked.
//
// The manifest is inside the signed tuple because the manifest is where the
// permissions live. A signature over the image alone would let somebody swap
// cpak.json, widen the sandbox and keep a signature that still verifies.
//
// A signed state proves the package came from the CI of that repository and
// was not altered on the way. It does not prove the software is safe, and it
// does not survive a compromised repository, because the repository is the
// identity being proven.
package signature

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ABIVersion changes when the meaning of a signed state changes, so a state
// written under one set of rules is never verified under another. It is not
// the integrity root ABI: the two describe different things and move for
// different reasons.
const ABIVersion = 1

// canonicalPrefix names the rules the canonical bytes were produced under. It
// is inside the signed message so that a payload made for one version of this
// format can never be replayed as another.
const canonicalPrefix = "cpak.signature.state.v1"

// ErrInvalidState reports a state that cannot mean anything, whatever was
// signed over it.
var ErrInvalidState = errors.New("signature: state is not well formed")

var (
	hostPattern        = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	segmentPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	sha256Pattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// State is what a publisher signs.
type State struct {
	ABI            int    `json:"abi"`
	Origin         string `json:"origin"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ImageDigest    string `json:"image_digest"`
	LockSHA256     string `json:"lock_sha256,omitempty"`
	Generation     uint64 `json:"generation"`
}

// Canonical is the exact byte string a publisher signs.
//
// It is a line format and not JSON on purpose. JSON has several ways to write
// the same object, and a second implementation of this format, in another
// language or in a signing tool nobody here wrote, has to agree with this one
// byte for byte or every signature it makes is worthless.
//
// The form is the prefix line followed by one key=value line per field, always
// in this order and always all of them, each line closed by a newline:
//
//	cpak.signature.state.v1
//	abi=1
//	origin=github.com/owner/repository
//	manifest_sha256=<64 lowercase hex characters>
//	image_digest=sha256:<64 lowercase hex characters>
//	lock_sha256=<64 lowercase hex characters, empty when there is no lock>
//	generation=<decimal, at least 1>
//
// Validate constrains every value to an alphabet that holds neither a newline
// nor an equals sign, so no escaping is defined and none is needed.
func (s State) Canonical() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var canonical bytes.Buffer
	canonical.WriteString(canonicalPrefix)
	canonical.WriteByte('\n')
	fmt.Fprintf(&canonical, "abi=%d\n", s.ABI)
	fmt.Fprintf(&canonical, "origin=%s\n", s.Origin)
	fmt.Fprintf(&canonical, "manifest_sha256=%s\n", s.ManifestSHA256)
	fmt.Fprintf(&canonical, "image_digest=%s\n", s.ImageDigest)
	fmt.Fprintf(&canonical, "lock_sha256=%s\n", s.LockSHA256)
	fmt.Fprintf(&canonical, "generation=%d\n", s.Generation)
	return canonical.Bytes(), nil
}

// Digest is the SHA-256 of the canonical encoding, lowercase hex and without an
// algorithm prefix, which is the shape the sha256 fields of a state carry. It
// is the message the signature is made over.
func (s State) Digest() (string, error) {
	sum, err := s.digest()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}

// Validate refuses a state that cannot be meaningful. Verify runs it before it
// opens a bundle, because a signature over something meaningless is still a
// valid signature and would otherwise be reported as a pass.
func (s State) Validate() error {
	if s.ABI != ABIVersion {
		return fmt.Errorf("%w: unsupported state abi %d", ErrInvalidState, s.ABI)
	}
	if origin, ok := canonicalOrigin(s.Origin); !ok || origin != s.Origin {
		return fmt.Errorf("%w: origin is not a lowercase host/owner/repository: %q", ErrInvalidState, s.Origin)
	}
	if !sha256Pattern.MatchString(s.ManifestSHA256) {
		return fmt.Errorf("%w: manifest hash is not a sha256: %q", ErrInvalidState, s.ManifestSHA256)
	}
	// A tag resolves to something different tomorrow, so a state that named one
	// would pin nothing. The signature is the pin, and a pin has to be a digest.
	if !imageDigestPattern.MatchString(s.ImageDigest) {
		return fmt.Errorf("%w: image reference is not a digest: %q", ErrInvalidState, s.ImageDigest)
	}
	if s.LockSHA256 != "" && !sha256Pattern.MatchString(s.LockSHA256) {
		return fmt.Errorf("%w: lock hash is not a sha256: %q", ErrInvalidState, s.LockSHA256)
	}
	if s.Generation == 0 {
		return fmt.Errorf("%w: generation 0 names no state", ErrInvalidState)
	}
	return nil
}

func (s State) digest() ([sha256.Size]byte, error) {
	canonical, err := s.Canonical()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

// canonicalOrigin folds an origin into the one shape a signed state may carry:
// a host, an owner and a repository, lowercase, and nothing else. It is the
// same shape cpak resolves a repository origin into.
//
// It reports failure rather than repairing what it was given. Two spellings of
// one origin must never both be signable, and a caller that cannot tell a
// rejected origin from a rewritten one would be comparing something it did not
// receive.
func canonicalOrigin(value string) (string, bool) {
	value = foldASCII(value)
	parts := strings.Split(value, "/")
	if len(parts) != 3 || !hostPattern.MatchString(parts[0]) {
		return "", false
	}
	if !segmentPattern.MatchString(parts[1]) || !segmentPattern.MatchString(parts[2]) {
		return "", false
	}
	return value, true
}

// foldASCII lowercases the ASCII letters and touches nothing else.
// strings.ToLower would fold characters such as the Kelvin sign onto a plain k,
// which is exactly the trick an origin comparison has to survive: a rune that
// is not an ASCII letter must stay what it is and be refused by the patterns
// above, never quietly become one.
func foldASCII(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}
