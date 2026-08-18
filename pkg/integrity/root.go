/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package integrity derives the roots an installed application is recognised
// by. Identity and policy are kept apart on purpose: what an application is
// changes only through an install or an update, while what it is allowed to do
// changes whenever its owner narrows it, and the two must not force each other
// to be re-enrolled.
package integrity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ABIVersion changes when the meaning of a root changes, so a root written by
// one cpak is never silently accepted by another that computes it differently.
const ABIVersion = 1

// LayerBinding ties a layer as the registry named it to the state the store
// actually produced for it. The link exists only for the instant that follows
// the download, and it is what makes a store state answerable to a digest.
type LayerBinding struct {
	OCIDigest string `json:"oci_digest"`
	StateID   string `json:"state_id"`
	StateRoot string `json:"state_root"`
}

// Package is the identity of an installed application.
type Package struct {
	Origin         string         `json:"origin"`
	Selector       string         `json:"selector"`
	Version        string         `json:"version"`
	ManifestDigest string         `json:"manifest_digest"`
	ImageDigest    string         `json:"image_digest"`
	ConfigDigest   string         `json:"config_digest"`
	Layers         []LayerBinding `json:"layers"`
	Dependencies   []string       `json:"dependencies"`
	Addons         []string       `json:"addons"`
	Binaries       []string       `json:"binaries"`
	DesktopEntries []string       `json:"desktop_entries"`
	Sessions       []string       `json:"sessions"`
}

var errUnboundLayer = errors.New("integrity: layer is not bound to a store state")

// Root hashes the identity. Layer order is preserved because it is the overlay
// order and carries meaning; the remaining lists are sets and are sorted, so
// that recording the same application twice produces the same root.
func (p Package) Root() (string, error) {
	if p.Origin == "" || p.ImageDigest == "" {
		return "", errors.New("integrity: package identity is incomplete")
	}
	for _, layer := range p.Layers {
		if layer.OCIDigest == "" || layer.StateID == "" || layer.StateRoot == "" {
			return "", fmt.Errorf("%w: %s", errUnboundLayer, layer.OCIDigest)
		}
	}
	canonical := p
	canonical.Dependencies = sorted(p.Dependencies)
	canonical.Addons = sorted(p.Addons)
	canonical.Binaries = sorted(p.Binaries)
	canonical.DesktopEntries = sorted(p.DesktopEntries)
	canonical.Sessions = sorted(p.Sessions)
	return digest("package", canonical)
}

// LaunchRoot is what a launch is checked against. It carries the ABI so a root
// computed under different rules never matches one recorded under these.
func LaunchRoot(packageRoot, policyRoot string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("cpak.launch.v%d\n%s\n%s\n", ABIVersion, packageRoot, policyRoot)))
	return hex.EncodeToString(sum[:])
}

func digest(kind string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	fmt.Fprintf(hash, "cpak.%s.v%d\n", kind, ABIVersion)
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copied := append([]string{}, values...)
	sort.Strings(copied)
	return copied
}
