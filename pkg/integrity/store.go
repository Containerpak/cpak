/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

import (
	"errors"
	"fmt"
	"regexp"
)

// The two records below are deliberately kept apart by who may write them. A
// binding is produced by the store and lives beside it, because it only claims
// what the store made out of a download. An anchor states what a launch is
// allowed to be, so it lives where the account running the launch cannot
// rewrite it.

// Bindings remembers, for a layer named by the registry, the state the store
// actually produced. Bind is called at the one moment the link is provable,
// which is the instant the download has been verified and committed.
type Bindings interface {
	Bind(binding LayerBinding) error
	Lookup(ociDigest string) (LayerBinding, bool, error)
}

var errAnchorDigest = errors.New("integrity: anchor digest is not a shape a signed state can name")

var (
	anchorImagePattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	anchorManifestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Anchor is the expected shape of a launch, recorded when an application is
// enrolled by an install or an update.
type Anchor struct {
	ABI        int    `json:"abi"`
	UID        uint32 `json:"uid"`
	Origin     string `json:"origin"`
	Generation uint64 `json:"generation"`

	// ImageDigest and ManifestDigest are the part of the identity a publisher
	// can answer for, stated on their own rather than only inside the package
	// root. A root is a hash of everything at once, so nothing can be compared
	// against it, and a signature that can only be compared against an origin
	// is satisfied by every genuine release of that origin, including the ones
	// this installation is not.
	//
	// They are derived from the installation and never copied out of the
	// signed state they are checked against, or the check would be comparing a
	// value with itself. ImageDigest is what the registry resolved the
	// reference to. ManifestDigest is the manifest the installation was
	// configured by, hashed the way a signed state hashes it, which is bare
	// hex and is not the spelling Package uses for the same object.
	//
	// Both are absent from a record written before they existed, and for such
	// a record no publisher signature was ever part of what the application is
	// recognised by.
	ImageDigest    string `json:"image_digest,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`

	PackageRoot string `json:"package_root"`
	PolicyRoot  string `json:"policy_root"`
	LaunchRoot  string `json:"launch_root"`
}

// ValidateDigests refuses a digest written in a shape no signed state can ever
// name, so that a value in the wrong spelling is refused where it is written
// instead of quietly never matching anything.
//
// It answers about shape and never about whether an anchor has to state these
// at all: that depends on whether a signature is being recorded with it, which
// is decided where signatures are.
func (a Anchor) ValidateDigests() error {
	if a.ImageDigest != "" && !anchorImagePattern.MatchString(a.ImageDigest) {
		return fmt.Errorf("%w: %q", errAnchorDigest, a.ImageDigest)
	}
	if a.ManifestDigest != "" && !anchorManifestPattern.MatchString(a.ManifestDigest) {
		return fmt.Errorf("%w: %q", errAnchorDigest, a.ManifestDigest)
	}
	return nil
}

// Anchors is the ledger of enrolled applications. A reader is available to
// anyone, a writer only to the privileged side.
type Anchors interface {
	Load(uid uint32, origin string) (Anchor, bool, error)
}

// AnchorWriter is implemented only where the ledger may be changed.
type AnchorWriter interface {
	Anchors
	Store(anchor Anchor) error
	Forget(uid uint32, origin string) error
}

// Ceiling is the widest policy an administrator allows on this host. An empty
// ceiling means the administrator has set none, and only the package and its
// owner decide.
type Ceiling interface {
	Allows(uid uint32, origin string) (allowed bool, ceiling any, err error)
}
