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
			Network:             true,
			DeviceDri:           true,
			FsHostHome:          true,
			FsExtra:             []string{"/games", "/child-only"},
			Env:                 []string{"SHARED=1", "CHILD=1"},
			AllowedHostCommands: []string{"xdg-open", "child-tool"},
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
			Network:             false,
			DeviceDri:           true,
			FsHostHome:          false,
			FsExtra:             []string{"/games", "/parent-only"},
			Env:                 []string{"SHARED=1", "PARENT=1"},
			AllowedHostCommands: []string{"xdg-open", "parent-tool"},
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
	if authorized.override.Network || authorized.override.FsHostHome {
		t.Fatalf("child gained parent-denied permissions: %+v", authorized.override)
	}
	if !authorized.override.DeviceDri {
		t.Fatal("a permission granted by both applications was dropped")
	}
	if len(authorized.override.FsExtra) != 1 || authorized.override.FsExtra[0] != "/games" {
		t.Fatalf("filesystem intersection: %v", authorized.override.FsExtra)
	}
	if len(authorized.override.Env) != 1 || authorized.override.Env[0] != "SHARED=1" {
		t.Fatalf("environment intersection: %v", authorized.override.Env)
	}
	if len(authorized.override.AllowedHostCommands) != 1 || authorized.override.AllowedHostCommands[0] != "xdg-open" {
		t.Fatalf("host command intersection: %v", authorized.override.AllowedHostCommands)
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
