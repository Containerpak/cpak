/*
 * Copyright (c) 2026 Marcin Jahn
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package imagetree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveAllRemovesReadOnlyImageDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this case relies on")
	}
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "kept"), []byte("kept"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(base, "layer")
	rootfs := filepath.Join(tree, "rootfs")
	for _, directory := range []string{"usr/bin", "usr/lib64/private", "root"} {
		if err := os.MkdirAll(filepath.Join(rootfs, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootfs, "usr/bin/WebKitWebDriver"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "usr/lib64/private/library"), []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootfs, "usr/bin/outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0o755) })
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{"usr/lib64/private", 0o000},
		{"usr/bin", 0o555},
		{"usr/lib64", 0o555},
		{"usr", 0o555},
		{"root", 0o555},
		{"", 0o555},
	} {
		if err := os.Chmod(filepath.Join(rootfs, directory.path), directory.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(tree); err == nil {
		t.Fatal("os.RemoveAll removed read-only directories, so this case proves nothing")
	}

	if err := RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("tree still exists: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("directory behind a symbolic link changed mode to %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(outside, "kept")); err != nil {
		t.Fatalf("file behind a symbolic link was removed: %v", err)
	}
}

func TestRemoveAllIgnoresMissingPaths(t *testing.T) {
	if err := RemoveAll(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}
