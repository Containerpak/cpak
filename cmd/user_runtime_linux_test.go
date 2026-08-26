/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUserRuntimeDirectoryIsPrivateAndWritable(t *testing.T) {
	if os.Getenv("CPAK_USER_RUNTIME_TEST") == "1" {
		runUserRuntimeDirectoryTest(t)
		return
	}
	command := exec.Command("unshare", "--user", "--map-root-user", "--mount", os.Args[0], "-test.run=^TestUserRuntimeDirectoryIsPrivateAndWritable$")
	command.Env = append(os.Environ(), "CPAK_USER_RUNTIME_TEST=1")
	if output, err := command.CombinedOutput(); err != nil {
		if bytes.Contains(output, []byte("/proc/self/uid_map: Operation not permitted")) {
			t.Skip("user namespaces are unavailable")
		}
		t.Fatalf("user runtime subprocess: %v\n%s", err, output)
	}
}

func runUserRuntimeDirectoryTest(t *testing.T) {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		t.Fatalf("make the mount tree private: %v", err)
	}
	rootFs := t.TempDir()
	runtimePath := filepath.Join(rootFs, "run/user/1000")
	if err := os.MkdirAll(runtimePath, 0755); err != nil {
		t.Fatalf("create stale runtime directory: %v", err)
	}
	sentinel := filepath.Join(runtimePath, "stale")
	if err := os.WriteFile(sentinel, nil, 0600); err != nil {
		t.Fatalf("write stale runtime state: %v", err)
	}

	grant, err := setupUserRuntimeDirectory(1000, rootFs)
	if err != nil {
		t.Fatalf("set up user runtime directory: %v", err)
	}
	defer syscall.Unmount(runtimePath, syscall.MNT_DETACH)
	if grant.Path != "/run/user/1000" || grant.ReadOnly {
		t.Fatalf("runtime grant: %+v", grant)
	}
	if _, err = os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale runtime state remains visible: %v", err)
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(runtimePath, &stat); err != nil {
		t.Fatalf("stat runtime filesystem: %v", err)
	}
	if stat.Type != unix.TMPFS_MAGIC {
		t.Fatalf("runtime filesystem type: got %#x, want tmpfs", stat.Type)
	}
	info, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatalf("stat runtime directory: %v", err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("runtime mode: got %#o, want 0700", info.Mode().Perm())
	}
	if err = os.WriteFile(filepath.Join(runtimePath, "dconf-user"), nil, 0600); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}
}

func TestUserRuntimeDirectoryRejectsInvalidUID(t *testing.T) {
	if _, err := setupUserRuntimeDirectory(-1, t.TempDir()); err == nil {
		t.Fatal("negative uid was accepted")
	}
}
