/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
	cp, parent, child, _ := nestedAuthFixtureWithToken(t)
	return cp, parent, child
}

func nestedAuthFixtureWithToken(t *testing.T) (*Cpak, types.Application, types.Application, string) {
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
			FilePicker: types.FilePickerGrant{
				OpenFile:         true,
				OpenFolder:       true,
				SaveFile:         true,
				Persistent:       true,
				ContainingFolder: true,
			},
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
			FilePicker: types.FilePickerGrant{
				OpenFile:         true,
				OpenFolder:       true,
				SaveFile:         false,
				Persistent:       true,
				ContainingFolder: true,
			},
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
	token, tokenErr := newNestedToken()
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	if err = store.NewContainer(types.Container{
		CpakId:            "parent-container",
		ApplicationCpakId: parent.CpakId,
		NestedToken:       token,
	}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	return &Cpak{Options: Options{StorePath: storePath}}, parent, child, token
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
	cp, _, _, nestedToken := nestedAuthFixtureWithToken(t)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  nestedToken,
		Origin: "github.com/example/not-declared",
		Binary: "tool",
	})
	if err == nil || !strings.Contains(err.Error(), "not a declared dependency") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthorizeNestedRunRejectsLayerDependency(t *testing.T) {
	cp, parent, child, nestedToken := nestedAuthFixtureWithToken(t)
	parent.ParsedDependencies[0].Mode = "layer"
	seedApplication(t, cp, parent)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  nestedToken,
		Origin: child.Origin,
		Binary: "child",
	})
	if err == nil || !strings.Contains(err.Error(), "not a declared dependency") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthorizeNestedRunDropsParentAndChildOnlyPermissions(t *testing.T) {
	cp, _, child, nestedToken := nestedAuthFixtureWithToken(t)
	authorized, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  nestedToken,
		Origin: child.Origin,
		Binary: "@/usr/bin/child",
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorized.child.CpakId != child.CpakId || authorized.binary != "/usr/bin/child" {
		t.Fatalf("authorized wrong child: %+v", authorized)
	}
	if authorized.policy.effective.Network {
		t.Fatalf("child gained parent-denied permissions: %+v", authorized.policy.effective)
	}
	if !authorized.policy.effective.DeviceDri {
		t.Fatal("a permission granted by both applications was dropped")
	}
	if !authorized.policy.effective.OpenURI {
		t.Fatal("a system broker permission granted by both applications was dropped")
	}
	if !authorized.policy.effective.UserNamespaces {
		t.Fatal("a nested namespace permission granted by both applications was dropped")
	}
	if len(authorized.policy.effective.Filesystem) != 2 || authorized.policy.effective.Filesystem[0] != (types.FilesystemPermission{Path: "/games/title", Access: "read-only"}) || authorized.policy.effective.Filesystem[1] != (types.FilesystemPermission{Path: "/shared", Access: "read-write"}) {
		t.Fatalf("filesystem intersection: %v", authorized.policy.effective.Filesystem)
	}
	if authorized.policy.effective.MemoryMaxMB != 512 || authorized.policy.effective.CPUQuota != 50 || authorized.policy.effective.PidsMax != 50 {
		t.Fatalf("resource limit intersection: %+v", authorized.policy.effective)
	}
	if len(authorized.policy.effective.Env) != 1 || authorized.policy.effective.Env[0] != "SHARED=1" {
		t.Fatalf("environment intersection: %v", authorized.policy.effective.Env)
	}
	if !authorized.policy.effective.FilePicker.OpenFile || !authorized.policy.effective.FilePicker.OpenFolder || authorized.policy.effective.FilePicker.SaveFile || !authorized.policy.effective.FilePicker.Persistent || !authorized.policy.effective.FilePicker.ContainingFolder {
		t.Fatalf("file picker intersection: %+v", authorized.policy.effective.FilePicker)
	}
	capabilities := types.HostActionCapabilities(authorized.policy.effective.HostActions, types.HostActionProviderContainers)
	if len(capabilities) != 1 || !capabilities[types.HostActionContainersRead] {
		t.Fatalf("host action intersection: %v", authorized.policy.effective.HostActions)
	}
}

func TestNestedDesktopCapabilitiesNeedBothApplications(t *testing.T) {
	parent := types.Override{DisplayX11: true, Bluetooth: false}
	child := types.Override{DisplayX11: true, Bluetooth: true}
	intersection := intersectOverrides(parent, child)
	if !intersection.DisplayX11 {
		t.Fatal("an isolated X11 display granted by both applications was dropped")
	}
	if intersection.Bluetooth {
		t.Fatal("a child gained Bluetooth that its parent did not grant")
	}
}

func TestNestedHostNetworkNeedsBothApplications(t *testing.T) {
	parent := types.Override{Network: true, HostNetwork: false}
	child := types.Override{Network: true, HostNetwork: true}
	if intersectOverrides(parent, child).HostNetwork {
		t.Fatal("a child gained host network access from its own policy")
	}
	parent.HostNetwork = true
	if !intersectOverrides(parent, child).HostNetwork {
		t.Fatal("host network access granted by both applications was dropped")
	}
}

func TestAuthorizeNestedRunRejectsUnexportedBinary(t *testing.T) {
	cp, _, child, nestedToken := nestedAuthFixtureWithToken(t)
	_, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  nestedToken,
		Origin: child.Origin,
		Binary: "@/bin/sh",
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

// The attack, end to end. An application identifier is public metadata, so a
// package with no permissions of its own can compute the identifier of one that
// has plenty and offer it as proof of being that application. It used to be
// believed, and the caller then ran one of the victim's declared dependencies
// under the victim's policy.
func TestNamingTheParentIsNotBeingIt(t *testing.T) {
	cp, parent, child, nestedToken := nestedAuthFixtureWithToken(t)

	// What the attacker can work out without ever seeing the victim.
	if _, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  parent.CpakId,
		Origin: child.Origin,
		Binary: "child",
	}); err == nil {
		t.Fatal("the parent's own identifier was accepted as proof of being the parent")
	}

	// And the capability the container really holds still works, or the fix
	// would be a lockout rather than a fix.
	if _, err := cp.authorizeNestedRun(types.RequestParams{
		Action: "run",
		Token:  nestedToken,
		Origin: child.Origin,
		Binary: "child",
	}); err != nil {
		t.Fatalf("a container's own capability was refused: %v", err)
	}
}
