/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestBuildManifestLockIncludesRecursiveDependencies(t *testing.T) {
	root := validManifestForTest()
	root.Dependencies = []types.Dependency{{Origin: "child", Branch: "stable"}}
	child := validManifestForTest()
	child.Name = "child"
	child.Image = "ghcr.io/example/child:stable"
	child.Dependencies = []types.Dependency{{Origin: "github.com/example/grandchild", Commit: "abc"}}
	grandchild := validManifestForTest()
	grandchild.Name = "grandchild"
	grandchild.Image = "ghcr.io/example/grandchild:abc"

	manifests := map[string]*types.CpakManifest{
		"github.com/example/child\x00stable\x00\x00":   child,
		"github.com/example/grandchild\x00\x00\x00abc": grandchild,
	}
	deps := manifestLockDeps{
		resolveImage: func(_ string, image string) (string, error) {
			return image + "@sha256:" + strings.Repeat("a", 64), nil
		},
		fetchManifest: func(origin, branch, release, commit string) (*types.CpakManifest, error) {
			return manifests[lockedPackageKey(origin, branch, release, commit)], nil
		},
	}
	cp := &Cpak{}
	lock, err := cp.buildManifestLock("github.com/example/root", root, deps)
	if err != nil {
		t.Fatal(err)
	}
	if lock.LockVersion != types.ManifestLockVersion || len(lock.Dependencies) != 2 {
		t.Fatalf("unexpected lock: %+v", lock)
	}
	wantOrigins := []string{"github.com/example/child", "github.com/example/grandchild"}
	gotOrigins := []string{lock.Dependencies[0].Origin, lock.Dependencies[1].Origin}
	if !reflect.DeepEqual(gotOrigins, wantOrigins) {
		t.Fatalf("dependency order: got %v, want %v", gotOrigins, wantOrigins)
	}
}

func TestBuildManifestLockRejectsDependencyCycle(t *testing.T) {
	root := validManifestForTest()
	root.Dependencies = []types.Dependency{{Origin: "github.com/example/child"}}
	child := validManifestForTest()
	child.Dependencies = []types.Dependency{{Origin: "github.com/example/child"}}
	deps := manifestLockDeps{
		resolveImage: func(_ string, image string) (string, error) {
			return image + "@sha256:" + strings.Repeat("a", 64), nil
		},
		fetchManifest: func(_, _, _, _ string) (*types.CpakManifest, error) { return child, nil },
	}
	if _, err := (&Cpak{}).buildManifestLock("github.com/example/root", root, deps); err == nil {
		t.Fatal("dependency cycle was accepted")
	}
}

func TestVerifyManifestLockUsesEmbeddedManifests(t *testing.T) {
	root := validManifestForTest()
	root.Dependencies = []types.Dependency{{Origin: "child"}}
	child := validManifestForTest()
	child.Name = "child"
	child.Image = "ghcr.io/example/child:main"
	deps := manifestLockDeps{
		resolveImage: func(_ string, image string) (string, error) {
			return image + "@sha256:" + strings.Repeat("b", 64), nil
		},
		fetchManifest: func(_, _, _, _ string) (*types.CpakManifest, error) { return child, nil },
	}
	cp := &Cpak{}
	lock, err := cp.buildManifestLock("github.com/example/root", root, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err = cp.VerifyManifestLock("github.com/example/root", root, lock); err != nil {
		t.Fatal(err)
	}
	lock.Dependencies[0].Manifest.Name = "tampered"
	if err = cp.VerifyManifestLock("github.com/example/root", root, lock); err == nil {
		t.Fatal("tampered embedded manifest was accepted")
	}
}
