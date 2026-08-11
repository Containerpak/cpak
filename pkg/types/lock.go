/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package types

// ManifestLockVersion is the current lock file format.
const ManifestLockVersion = "1.0"

// ManifestLock contains the immutable root and dependency graph.
type ManifestLock struct {
	LockVersion  string          `json:"lock_version"`
	Root         LockedPackage   `json:"root"`
	Dependencies []LockedPackage `json:"dependencies"`
}

// LockedPackage records a manifest and its immutable OCI image reference.
type LockedPackage struct {
	Origin         string        `json:"origin,omitempty"`
	Branch         string        `json:"branch,omitempty"`
	Commit         string        `json:"commit,omitempty"`
	Release        string        `json:"release,omitempty"`
	ManifestSHA256 string        `json:"manifest_sha256"`
	Image          string        `json:"image"`
	ResolvedImage  string        `json:"resolved_image"`
	Manifest       *CpakManifest `json:"manifest"`
}
