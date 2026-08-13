/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package tools

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrepareRootfsTargetCreatesDirectoryAndFile(t *testing.T) {
	rootfs := t.TempDir()
	directory, err := PrepareRootfsTarget(rootfs, "/run/cpak", RootfsTargetDirectory)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		t.Fatalf("directory target: %v, %v", info, err)
	}
	file, err := PrepareRootfsTarget(rootfs, "/run/cpak/service.sock", RootfsTargetFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(file)
	if err != nil || info.IsDir() {
		t.Fatalf("file target: %v, %v", info, err)
	}
}

func TestPrepareRootfsTargetRejectsSymlinkEscape(t *testing.T) {
	rootfs := t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(rootfs, "run")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(rootfs, "/run/cpak", RootfsTargetDirectory); err == nil {
		t.Fatal("accepted intermediate symlink")
	}

	if err := os.Remove(filepath.Join(rootfs, "run")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootfs, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(rootfs, "run", "cpak")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(rootfs, "/run/cpak", RootfsTargetDirectory); err == nil {
		t.Fatal("accepted final symlink")
	}
}

func TestPrepareRootfsTargetRejectsSymlinkRootfs(t *testing.T) {
	rootfs := t.TempDir()
	link := filepath.Join(t.TempDir(), "rootfs")
	if err := os.Symlink(rootfs, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(link, "/run/cpak", RootfsTargetDirectory); err == nil {
		t.Fatal("accepted symlink rootfs")
	}
}

func TestPrepareRootfsTargetRejectsInvalidAndMismatchedTargets(t *testing.T) {
	rootfs := t.TempDir()
	for _, target := range []string{"/", "/run/../etc", "/run/./cpak"} {
		if _, err := PrepareRootfsTarget(rootfs, target, RootfsTargetDirectory); err == nil {
			t.Fatalf("accepted invalid target %q", target)
		}
	}
	if _, err := PrepareRootfsTarget(rootfs, "/etc/hosts", RootfsTargetFile); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(rootfs, "/etc/hosts", RootfsTargetDirectory); err == nil {
		t.Fatal("accepted file as directory")
	}
	if _, err := PrepareRootfsTarget(rootfs, "/var/lib", RootfsTargetDirectory); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(rootfs, "/var/lib", RootfsTargetFile); err == nil {
		t.Fatal("accepted directory as file")
	}
	pipe := filepath.Join(rootfs, "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsTarget(rootfs, "/pipe", RootfsTargetFile); err == nil {
		t.Fatal("accepted FIFO as file")
	}
}

func TestMountPreparedRejectsUnsafeDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := MountPrepared(source, link, 0); err == nil {
		t.Fatal("accepted symlink mount destination")
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := MountPrepared(source, directory, 0); err == nil {
		t.Fatal("accepted mismatched mount destination")
	}
}

func TestMountDevptsPreparedRejectsUnsafeDestination(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := MountDevptsPrepared(file); err == nil {
		t.Fatal("accepted file devpts destination")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if err := MountDevptsPrepared(link); err == nil {
		t.Fatal("accepted symlink devpts destination")
	}
}

func TestPrepareRootfsReplacementFileReplacesFinalSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/share/zoneinfo/Etc/UTC", filepath.Join(root, "etc", "localtime")); err != nil {
		t.Fatal(err)
	}
	path, err := PrepareRootfsReplacementFile(root, "/etc/localtime")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("replacement is not a regular file: %v %v", info, err)
	}
}

func TestPrepareRootfsReplacementFileRejectsParentSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareRootfsReplacementFile(root, "/etc/localtime"); err == nil {
		t.Fatal("parent symlink was followed")
	}
	if _, err := os.Stat(filepath.Join(outside, "localtime")); !os.IsNotExist(err) {
		t.Fatalf("file escaped the rootfs: %v", err)
	}
}
