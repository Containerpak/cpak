/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func nestedAuthFixture(t *testing.T) (*Cpak, types.Application, types.Application) {
	t.Helper()
	storePath := t.TempDir()
	child := types.Application{
		CpakId:         "child-id",
		Name:           "Child",
		Origin:         "github.com/example/child",
		Branch:         "main",
		Version:        "1",
		ParsedBinaries: []string{"/usr/bin/child"},
		ParsedOverride: types.Override{
			Network:        true,
			DeviceDri:      true,
			OpenURI:        true,
			UserNamespaces: true,
			Filesystem: []types.FilesystemPermission{
				{Path: "/games/title", Access: "read-write"},
				{Path: "/shared", Access: "read-write"},
				{Path: "/child-only", Access: "read-write"},
			},
			MemoryMaxMB: 1024,
			PidsMax:     50,
			Env:         []string{"SHARED=1", "CHILD=1"},
			HostActions: []types.HostActionGrant{{
				Provider:     types.HostActionProviderContainers,
				Capabilities: []string{types.HostActionContainersRead, types.HostActionContainersExecOwned},
			}},
		},
	}
	parent := types.Application{
		CpakId:  "parent-id",
		Name:    "Parent",
		Origin:  "github.com/example/parent",
		Version: "1",
		ParsedDependencies: []types.Dependency{{
			Id:     child.CpakId,
			Origin: child.Origin,
			Branch: child.Branch,
		}},
		ParsedOverride: types.Override{
			Network:        false,
			DeviceDri:      true,
			OpenURI:        true,
			UserNamespaces: true,
			Filesystem: []types.FilesystemPermission{
				{Path: "/games", Access: "read-only"},
				{Path: "/shared", Access: "read-write"},
				{Path: "/parent-only", Access: "read-write"},
			},
			MemoryMaxMB: 512,
			CPUQuota:    50,
			PidsMax:     100,
			Env:         []string{"SHARED=1", "PARENT=1"},
			HostActions: []types.HostActionGrant{{
				Provider:     types.HostActionProviderContainers,
				Capabilities: []string{types.HostActionContainersRead, types.HostActionContainersManageOwned},
			}},
		},
	}
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.NewApplication(parent); err != nil {
		t.Fatal(err)
	}
	if err = store.NewApplication(child); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	return &Cpak{Options: types.CpakOptions{StorePath: storePath}}, parent, child
}

func TestDependencyLinksExposeOnlyDeclaredExports(t *testing.T) {
	cp, parent, _ := nestedAuthFixture(t)
	cp.Options.ExportsPath = t.TempDir()
	source := filepath.Join(cp.Options.ExportsPath, "github.com", "example", "child", "child")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	links, err := cp.dependencyLinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	want := source + ":/usr/local/bin/child"
	if len(links) != 1 || links[0] != want {
		t.Fatalf("got %v, want %s", links, want)
	}
}

func TestDependencyLinksSkipLayerDependencies(t *testing.T) {
	cp, parent, _ := nestedAuthFixture(t)
	parent.ParsedDependencies[0].Mode = "layer"
	links, err := cp.dependencyLinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("layer dependency exports nested shims: %v", links)
	}
}

func TestAuthorizeNestedRunRejectsUndeclaredChild(t *testing.T) {
	cp, parent, _ := nestedAuthFixture(t)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action:      "run",
		ParentAppId: parent.CpakId,
		Origin:      "github.com/example/not-declared",
		Binary:      "tool",
	})
	if err == nil || !strings.Contains(err.Error(), "not a declared dependency") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthorizeNestedRunRejectsLayerDependency(t *testing.T) {
	cp, parent, child := nestedAuthFixture(t)
	parent.ParsedDependencies[0].Mode = "layer"
	seedApplication(t, cp, parent)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action:      "run",
		ParentAppId: parent.CpakId,
		Origin:      child.Origin,
		Binary:      "child",
	})
	if err == nil || !strings.Contains(err.Error(), "not a declared dependency") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthorizeNestedRunDropsParentAndChildOnlyPermissions(t *testing.T) {
	cp, parent, child := nestedAuthFixture(t)
	authorized, err := cp.authorizeNestedRun(types.RequestParams{
		Action:      "run",
		ParentAppId: parent.CpakId,
		Origin:      child.Origin,
		Binary:      "@/usr/bin/child",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.child.CpakId != child.CpakId || authorized.binary != "/usr/bin/child" {
		t.Fatalf("authorized wrong child: %+v", authorized)
	}
	if authorized.override.Network {
		t.Fatalf("child gained parent-denied permissions: %+v", authorized.override)
	}
	if !authorized.override.DeviceDri {
		t.Fatal("a permission granted by both applications was dropped")
	}
	if !authorized.override.OpenURI {
		t.Fatal("a system broker permission granted by both applications was dropped")
	}
	if !authorized.override.UserNamespaces {
		t.Fatal("a nested namespace permission granted by both applications was dropped")
	}
	if len(authorized.override.Filesystem) != 2 || authorized.override.Filesystem[0] != (types.FilesystemPermission{Path: "/games/title", Access: "read-only"}) || authorized.override.Filesystem[1] != (types.FilesystemPermission{Path: "/shared", Access: "read-write"}) {
		t.Fatalf("filesystem intersection: %v", authorized.override.Filesystem)
	}
	if authorized.override.MemoryMaxMB != 512 || authorized.override.CPUQuota != 50 || authorized.override.PidsMax != 50 {
		t.Fatalf("resource limit intersection: %+v", authorized.override)
	}
	if len(authorized.override.Env) != 1 || authorized.override.Env[0] != "SHARED=1" {
		t.Fatalf("environment intersection: %v", authorized.override.Env)
	}
	capabilities := types.HostActionCapabilities(authorized.override.HostActions, types.HostActionProviderContainers)
	if len(capabilities) != 1 || !capabilities[types.HostActionContainersRead] {
		t.Fatalf("host action intersection: %v", authorized.override.HostActions)
	}
}

func TestAuthorizeNestedRunRejectsUnexportedBinary(t *testing.T) {
	cp, parent, child := nestedAuthFixture(t)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action:      "run",
		ParentAppId: parent.CpakId,
		Origin:      child.Origin,
		Binary:      "@/bin/sh",
	})
	if err == nil || !strings.Contains(err.Error(), "is not exported") {
		t.Fatalf("got %v", err)
	}
}

func TestIntersectFilesystemPortableScopes(t *testing.T) {
	homeOnly := intersectFilesystem(
		[]types.FilesystemPermission{{Path: "home", Access: "read-write"}},
		[]types.FilesystemPermission{{Path: "home", Access: "read-write"}, {Path: "host", Access: "read-only"}},
	)
	if len(homeOnly) != 1 || homeOnly[0] != (types.FilesystemPermission{Path: "home", Access: "read-write"}) {
		t.Fatalf("home scope intersection: %v", homeOnly)
	}

	hostReadOnly := intersectFilesystem(
		[]types.FilesystemPermission{{Path: "host", Access: "read-only"}},
		[]types.FilesystemPermission{{Path: "home", Access: "read-write"}, {Path: "/tmp", Access: "read-write"}},
	)
	if len(hostReadOnly) != 2 || hostReadOnly[0] != (types.FilesystemPermission{Path: "/tmp", Access: "read-only"}) || hostReadOnly[1] != (types.FilesystemPermission{Path: "home", Access: "read-only"}) {
		t.Fatalf("host scope intersection: %v", hostReadOnly)
	}
}
