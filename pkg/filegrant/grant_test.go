/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package filegrant

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveFileAndContainingFolder(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "game.exe")
	if err := os.WriteFile(selected, []byte("MZ"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := Resolve("github.com/example/app", selected, AccessReadOnly, LifetimeSession, false)
	if err != nil {
		t.Fatal(err)
	}
	if file.Source != selected || file.Kind != KindFile || filepath.Base(file.Target) != "game.exe" {
		t.Fatalf("file grant: %+v", file)
	}
	folder, err := Resolve("github.com/example/app", selected, AccessReadOnly, LifetimePersistent, true)
	if err != nil {
		t.Fatal(err)
	}
	if folder.Source != directory || folder.Kind != KindDirectory || filepath.Base(folder.MountTarget) != filepath.Base(directory) || filepath.Base(folder.Target) != "game.exe" {
		t.Fatalf("folder grant: %+v", folder)
	}
}

func TestResolveSaveMountsTheSelectedDirectory(t *testing.T) {
	directory := t.TempDir()
	grant, err := ResolveSave("github.com/example/app", filepath.Join(directory, "report.pdf"), LifetimeSession)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Source != directory || grant.Kind != KindDirectory || grant.Access != AccessReadWrite || filepath.Base(grant.Target) != "report.pdf" {
		t.Fatalf("save grant: %+v", grant)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(selected, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	grant, err := Resolve("github.com/example/app", selected, AccessReadOnly, LifetimePersistent, false)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Directory: filepath.Join(t.TempDir(), "grants")}
	if err = store.Add(grant); err != nil {
		t.Fatal(err)
	}
	grants, err := store.Load(grant.Origin)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0] != grant {
		t.Fatalf("stored grants: %+v", grants)
	}
	if err = store.Remove(grant.Origin, grant.ID); err != nil {
		t.Fatal(err)
	}
	grants, err = store.Load(grant.Origin)
	if err != nil || len(grants) != 0 {
		t.Fatalf("removed grants: %+v, %v", grants, err)
	}
}

func TestOpenSourceRejectsSymlinkSubstitution(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "document.pdf")
	replacement := filepath.Join(directory, "replacement.pdf")
	if err := os.WriteFile(selected, []byte("selected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	grant, err := Resolve("github.com/example/app", selected, AccessReadOnly, LifetimeSession, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(selected); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(replacement, selected); err != nil {
		t.Fatal(err)
	}
	if source, openErr := OpenSource(grant); openErr == nil {
		_ = source.Close()
		t.Fatal("symlink substitution was accepted")
	}
}

func TestGrantValidationRejectsScopeMutation(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(selected, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	grant, err := Resolve("github.com/example/app", selected, AccessReadOnly, LifetimeSession, false)
	if err != nil {
		t.Fatal(err)
	}
	grant.Access = AccessReadWrite
	if err = grant.Validate(); err == nil {
		t.Fatal("mutated grant scope was accepted")
	}
}
