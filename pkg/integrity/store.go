/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

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

// Anchor is the expected shape of a launch, recorded when an application is
// enrolled by an install or an update.
type Anchor struct {
	ABI         int    `json:"abi"`
	UID         uint32 `json:"uid"`
	Origin      string `json:"origin"`
	Generation  uint64 `json:"generation"`
	PackageRoot string `json:"package_root"`
	PolicyRoot  string `json:"policy_root"`
	LaunchRoot  string `json:"launch_root"`
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
