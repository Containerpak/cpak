/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/registryauth"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type manifestLockDeps struct {
	resolveImage  func(string, string) (string, error)
	fetchManifest func(string, string, string, string) (*types.CpakManifest, error)
}

// BuildManifestLock resolves the package graph to immutable OCI digests.
func (c *Cpak) BuildManifestLock(origin string, manifest *types.CpakManifest) (types.ManifestLock, error) {
	deps := manifestLockDeps{
		resolveImage: func(origin, image string) (string, error) {
			ref, err := oci.ParseReference(image)
			if err != nil {
				return "", err
			}
			client := &oci.Client{Credentials: registryauth.Provider{Origin: origin, Path: c.Options.RegistryAuthPath}}
			digest, err := client.Digest(c.Ctx, image)
			if err != nil {
				return "", err
			}
			return ref.ContextName() + "@" + digest, nil
		},
		fetchManifest: c.FetchManifest,
	}
	return c.buildManifestLock(origin, manifest, deps)
}

func (c *Cpak) buildManifestLock(origin string, manifest *types.CpakManifest, deps manifestLockDeps) (types.ManifestLock, error) {
	if manifest == nil {
		return types.ManifestLock{}, fmt.Errorf("manifest is required")
	}
	if err := c.ValidateManifest(manifest); err != nil {
		return types.ManifestLock{}, err
	}
	if strings.TrimSpace(origin) == "" {
		return types.ManifestLock{}, fmt.Errorf("origin is required")
	}
	root, err := lockPackage(origin, "", "", "", manifest, deps.resolveImage)
	if err != nil {
		return types.ManifestLock{}, err
	}
	lock := types.ManifestLock{LockVersion: types.ManifestLockVersion, Root: root, Dependencies: []types.LockedPackage{}}
	visited := map[string]bool{}
	visiting := map[string]bool{}

	var visit func(string, *types.CpakManifest) error
	visit = func(parentOrigin string, parent *types.CpakManifest) error {
		for _, dependency := range parent.Dependencies {
			dependencyOrigin, resolveErr := resolveDependencyOrigin(parentOrigin, dependency.Origin)
			if resolveErr != nil {
				return resolveErr
			}
			branch, release, commit := dependencySelectors(dependency)
			key := lockedPackageKey(dependencyOrigin, branch, release, commit)
			if visiting[key] {
				return fmt.Errorf("dependency cycle detected at %s", key)
			}
			if visited[key] {
				continue
			}
			visiting[key] = true
			dependencyManifest, fetchErr := deps.fetchManifest(dependencyOrigin, branch, release, commit)
			if fetchErr != nil {
				return fmt.Errorf("lock dependency %s: %w", dependencyOrigin, fetchErr)
			}
			if dependencyManifest == nil {
				return fmt.Errorf("lock dependency %s: no manifest returned", dependencyOrigin)
			}
			if validateErr := c.ValidateManifest(dependencyManifest); validateErr != nil {
				return fmt.Errorf("lock dependency %s: %w", dependencyOrigin, validateErr)
			}
			locked, lockErr := lockPackage(dependencyOrigin, branch, release, commit, dependencyManifest, deps.resolveImage)
			if lockErr != nil {
				return fmt.Errorf("lock dependency %s: %w", dependencyOrigin, lockErr)
			}
			lock.Dependencies = append(lock.Dependencies, locked)
			if visitErr := visit(dependencyOrigin, dependencyManifest); visitErr != nil {
				return visitErr
			}
			delete(visiting, key)
			visited[key] = true
		}
		return nil
	}

	if err = visit(origin, manifest); err != nil {
		return types.ManifestLock{}, err
	}
	sort.Slice(lock.Dependencies, func(i, j int) bool {
		left := lock.Dependencies[i]
		right := lock.Dependencies[j]
		return lockedPackageKey(left.Origin, left.Branch, left.Release, left.Commit) < lockedPackageKey(right.Origin, right.Branch, right.Release, right.Commit)
	})
	return lock, nil
}

// VerifyManifestLock validates an embedded package graph without network access.
func (c *Cpak) VerifyManifestLock(origin string, manifest *types.CpakManifest, lock types.ManifestLock) error {
	if lock.LockVersion != types.ManifestLockVersion {
		return fmt.Errorf("unsupported lock version: %s", lock.LockVersion)
	}
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	if lock.Root.Origin != origin {
		return fmt.Errorf("lock root origin is %s, expected %s", lock.Root.Origin, origin)
	}
	packages := make(map[string]types.LockedPackage, len(lock.Dependencies)+1)
	all := append([]types.LockedPackage{lock.Root}, lock.Dependencies...)
	for _, locked := range all {
		key := lockedPackageKey(locked.Origin, locked.Branch, locked.Release, locked.Commit)
		if _, exists := packages[key]; exists {
			return fmt.Errorf("duplicate locked package: %s", locked.Origin)
		}
		if err := c.validateLockedPackage(locked); err != nil {
			return err
		}
		packages[key] = locked
	}
	rootDigest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	if rootDigest != lock.Root.ManifestSHA256 {
		return fmt.Errorf("cpak.lock.json does not match the root manifest")
	}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(types.LockedPackage) error
	visit = func(parent types.LockedPackage) error {
		parentKey := lockedPackageKey(parent.Origin, parent.Branch, parent.Release, parent.Commit)
		if visiting[parentKey] {
			return fmt.Errorf("dependency cycle detected at %s", parent.Origin)
		}
		if seen[parentKey] {
			return nil
		}
		visiting[parentKey] = true
		for _, dependency := range parent.Manifest.Dependencies {
			dependencyOrigin, resolveErr := resolveDependencyOrigin(parent.Origin, dependency.Origin)
			if resolveErr != nil {
				return resolveErr
			}
			branch, release, commit := dependencySelectors(dependency)
			key := lockedPackageKey(dependencyOrigin, branch, release, commit)
			locked, exists := packages[key]
			if !exists {
				return fmt.Errorf("dependency is missing from lock: %s", dependencyOrigin)
			}
			if err := visit(locked); err != nil {
				return err
			}
		}
		delete(visiting, parentKey)
		seen[parentKey] = true
		return nil
	}
	if err = visit(lock.Root); err != nil {
		return err
	}
	if len(seen) != len(packages) {
		return fmt.Errorf("cpak.lock.json contains unreachable packages")
	}
	return nil
}

func (c *Cpak) validateLockedPackage(locked types.LockedPackage) error {
	if locked.Manifest == nil {
		return fmt.Errorf("locked package %s has no manifest", locked.Origin)
	}
	if err := c.ValidateManifest(locked.Manifest); err != nil {
		return fmt.Errorf("invalid locked manifest for %s: %w", locked.Origin, err)
	}
	digest, err := manifestDigest(locked.Manifest)
	if err != nil {
		return err
	}
	if digest != locked.ManifestSHA256 || locked.Manifest.Image != locked.Image {
		return fmt.Errorf("locked package %s has inconsistent manifest data", locked.Origin)
	}
	ref, err := oci.ParseReference(locked.ResolvedImage)
	if err != nil {
		return fmt.Errorf("invalid resolved image for %s: %w", locked.Origin, err)
	}
	if !ref.IsDigest {
		return fmt.Errorf("resolved image for %s is not pinned by digest", locked.Origin)
	}
	return nil
}

func lockPackage(origin, branch, release, commit string, manifest *types.CpakManifest, resolveImage func(string, string) (string, error)) (types.LockedPackage, error) {
	digest, err := manifestDigest(manifest)
	if err != nil {
		return types.LockedPackage{}, err
	}
	image, err := resolveManifestImage(manifest, branch, release, commit)
	if err != nil {
		return types.LockedPackage{}, fmt.Errorf("resolve image reference: %w", err)
	}
	resolved, err := resolveImage(origin, image)
	if err != nil {
		return types.LockedPackage{}, fmt.Errorf("resolve image %s: %w", image, err)
	}
	return types.LockedPackage{
		Origin:         origin,
		Branch:         branch,
		Commit:         commit,
		Release:        release,
		ManifestSHA256: digest,
		Image:          manifest.Image,
		ResolvedImage:  resolved,
		Manifest:       manifest,
	}, nil
}

func lockedPackageFromManifestLock(lock *types.ManifestLock, origin, branch, release, commit string) (types.LockedPackage, bool) {
	if lock == nil {
		return types.LockedPackage{}, false
	}
	key := lockedPackageKey(origin, branch, release, commit)
	if lockedPackageKey(lock.Root.Origin, lock.Root.Branch, lock.Root.Release, lock.Root.Commit) == key {
		return lock.Root, true
	}
	for _, dependency := range lock.Dependencies {
		if lockedPackageKey(dependency.Origin, dependency.Branch, dependency.Release, dependency.Commit) == key {
			return dependency, true
		}
	}
	return types.LockedPackage{}, false
}

func manifestDigest(manifest *types.CpakManifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func resolveDependencyOrigin(parentOrigin, dependencyOrigin string) (string, error) {
	if isURL(dependencyOrigin) {
		return strings.ToLower(dependencyOrigin), nil
	}
	separator := strings.LastIndex(parentOrigin, "/")
	if separator < 0 {
		return "", fmt.Errorf("relative dependency %s requires a root origin", dependencyOrigin)
	}
	return strings.ToLower(parentOrigin[:separator] + "/" + dependencyOrigin), nil
}

func lockedPackageKey(origin, branch, release, commit string) string {
	return strings.Join([]string{strings.ToLower(origin), branch, release, commit}, "\x00")
}
