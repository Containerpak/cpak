/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestWorkingDirectoryUsesOnlyExistingGrants(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "Projects", "example")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	c := Cpak{}
	policy := types.NewOverride()
	if got, err := c.runtimeWorkingDirectory(policy); err != nil || got != "" {
		t.Fatalf("unshared current directory was selected: %q %v", got, err)
	}
	policy.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	if got, err := c.runtimeWorkingDirectory(policy); err != nil || got != project {
		t.Fatalf("shared current directory was lost: %q %v", got, err)
	}
	policy.Filesystem = []types.FilesystemPermission{{Path: "host", Access: "read-only"}}
	if got, err := c.runtimeWorkingDirectory(policy); err != nil || got != filepath.Join("/run/host", project) {
		t.Fatalf("host grant did not map the directory: %q %v", got, err)
	}
	policy.Filesystem = append(policy.Filesystem, types.FilesystemPermission{Path: "home", Access: "read-write"})
	if got, err := c.runtimeWorkingDirectory(policy); err != nil || got != project {
		t.Fatalf("a broader grant took precedence over home: %q %v", got, err)
	}
	if err := c.SetWorkingDirectory("."); err != nil {
		t.Fatal(err)
	}
	if _, err := c.runtimeWorkingDirectory(types.NewOverride()); err == nil {
		t.Fatal("an explicit directory added a filesystem grant")
	}
	if len(policy.Filesystem) != 2 {
		t.Fatal("selecting a directory mutated filesystem policy")
	}
}

func TestWorkingDirectoryRefusesStateAndUnsharedSymlinks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := filepath.Join(home, "shared")
	state := filepath.Join(home, ".local", "share", "applications")
	outside := t.TempDir()
	for _, directory := range []string{shared, state} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(shared, "outside")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	c := Cpak{}
	for _, directory := range []string{link, state} {
		if err := c.SetWorkingDirectory(directory); err != nil {
			t.Fatal(err)
		}
		policy := types.Override{Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-write"}}}
		if _, err := c.runtimeWorkingDirectory(policy); err == nil {
			t.Fatalf("accepted an unshared or masked directory: %s", directory)
		}
	}
}
