/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func requireRestrictedResolution(t *testing.T) {
	t.Helper()
	err := CheckResolveSupport()
	if err == nil {
		return
	}
	if errors.Is(err, ErrResolveUnsupported) {
		t.Skip("openat2 is blocked or missing on this kernel, the restricted resolution cannot be exercised here")
	}
	t.Fatalf("the restricted resolution failed for a reason other than a missing syscall: %v", err)
}

func TestOpenBeneathResolvesBeneathTheRoot(t *testing.T) {
	requireRestrictedResolution(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "usr", "bin", "tool")
	if err := os.WriteFile(target, []byte("tool"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}

	fd, err := OpenBeneath(root, "usr/bin/tool", unix.O_PATH)
	if err != nil {
		t.Fatalf("a path that stays beneath the root was refused: %v", err)
	}
	defer unix.Close(fd)

	identity, err := IdentifyDescriptor(fd)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("the platform did not report a stat for the target")
	}
	if identity.Device != uint64(stat.Dev) || identity.Inode != uint64(stat.Ino) {
		t.Fatalf("the descriptor names %s, which is not the file that was asked for", identity)
	}
	if identity.Kind != DescriptorKindRegular {
		t.Fatalf("a regular file was reported as a %s", identity.Kind)
	}
	if identity.Mode != 0o755 {
		t.Fatalf("the identity carries mode %04o, so a permission change would go unnoticed", identity.Mode)
	}
}

func TestOpenBeneathRefusesEveryEscape(t *testing.T) {
	requireRestrictedResolution(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../"+filepath.Base(outside), filepath.Join(root, "sub", "up")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"an absolute path", "/etc/passwd"},
		{"a parent traversal", "../" + filepath.Base(outside)},
		{"a symlinked directory in the middle", "link/secret"},
		{"a symlink as the last component", "link"},
		{"a relative symlink that climbs out", "sub/up"},
	}
	for _, c := range cases {
		fd, err := OpenBeneath(root, c.path, unix.O_PATH)
		if err == nil {
			unix.Close(fd)
			t.Fatalf("%s reached outside the trusted root", c.name)
		}
		if errors.Is(err, ErrResolveUnsupported) {
			t.Fatalf("%s was refused as a missing syscall, which hides the escape from the caller", c.name)
		}
	}
}

func TestOpenBeneathReportsAMissingPathAsSuch(t *testing.T) {
	requireRestrictedResolution(t)
	root := t.TempDir()
	if _, err := OpenBeneath(root, "absent", unix.O_PATH); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("got %v, want the kernel answer for a path that is simply not there", err)
	}
}

func TestOpenBeneathNeverCreates(t *testing.T) {
	root := t.TempDir()
	fd, err := OpenBeneath(root, "new", unix.O_WRONLY|unix.O_CREAT)
	if err == nil {
		unix.Close(fd)
		t.Fatal("a resolution helper was allowed to create a file")
	}
	if _, err = os.Lstat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("the refused resolution left something behind: %v", err)
	}
}

func TestOpenBeneathRefusesAnUntrustedRoot(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if fd, err := OpenBeneath(link, ".", unix.O_PATH|unix.O_DIRECTORY); err == nil {
		unix.Close(fd)
		t.Fatal("a symlinked trust anchor was accepted")
	}
	if fd, err := OpenBeneath("relative/root", ".", unix.O_PATH|unix.O_DIRECTORY); err == nil {
		unix.Close(fd)
		t.Fatal("a relative trust anchor was accepted")
	}
}

func TestIdentifyDescriptorTellsObjectsApart(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	before := identify(t, first)
	if !before.Same(identify(t, first)) {
		t.Fatalf("the same file reported two identities, %s and %s", before, identify(t, first))
	}
	if before.Same(identify(t, second)) {
		t.Fatal("two different files share one identity, so a swap would go unnoticed")
	}
	if err := os.Chmod(first, 0o755); err != nil {
		t.Fatal(err)
	}
	if before.Same(identify(t, first)) {
		t.Fatal("a permission change left the identity untouched")
	}
	if kind := identify(t, root).Kind; kind != DescriptorKindDirectory {
		t.Fatalf("a directory was reported as a %s", kind)
	}
}

func TestCloneDescriptorMountKeepsTheVerifiedObject(t *testing.T) {
	requireRestrictedResolution(t)
	root := t.TempDir()
	fd, err := OpenBeneath(root, ".", unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	verified, err := IdentifyDescriptor(fd)
	if err != nil {
		t.Fatal(err)
	}

	treeFD, err := CloneDescriptorMount(fd, false)
	if err != nil {
		if errors.Is(err, ErrDetachedMountUnsupported) {
			t.Skip("open_tree is blocked or unprivileged here, a detached mount cannot be exercised")
		}
		t.Fatalf("cloning a verified descriptor failed: %v", err)
	}
	defer unix.Close(treeFD)

	attached, err := IdentifyDescriptor(treeFD)
	if err != nil {
		t.Fatal(err)
	}
	if !attached.Same(verified) {
		t.Fatalf("the clone names %s while %s was verified, so the mount would carry another object", attached, verified)
	}
}

func TestCloneDescriptorMountAttachesTheVerifiedObject(t *testing.T) {
	requireRestrictedResolution(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "marker"), []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	fd, err := OpenBeneath(root, "source", unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	treeFD, err := CloneDescriptorMount(fd, false)
	if err != nil {
		if errors.Is(err, ErrDetachedMountUnsupported) {
			t.Skip("open_tree is blocked or unprivileged here, a detached mount cannot be exercised")
		}
		t.Fatal(err)
	}
	defer unix.Close(treeFD)

	if err = AttachDescriptorMountPrepared(treeFD, destination); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) {
			t.Skip("move_mount is blocked or unprivileged here, the clone cannot be attached")
		}
		t.Fatalf("a cloned descriptor was not mountable: %v", err)
	}
	defer unix.Unmount(destination, unix.MNT_DETACH)

	if _, err = os.Stat(filepath.Join(destination, "marker")); err != nil {
		t.Fatalf("the attached mount does not carry the object that was verified: %v", err)
	}
}

func TestUnsupportedSyscallCoversOnlyTheMissingGuarantee(t *testing.T) {
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM, unix.EOPNOTSUPP} {
		if !unsupportedSyscall(errno) {
			t.Fatalf("%v would be reported as a path failure, which invites a fallback to a plain open", errno)
		}
	}
	for _, errno := range []unix.Errno{unix.EXDEV, unix.ELOOP, unix.ENOENT, unix.EACCES} {
		if unsupportedSyscall(errno) {
			t.Fatalf("%v is a refused path and must not be reported as a missing kernel guarantee", errno)
		}
	}
}

func identify(t *testing.T, path string) DescriptorIdentity {
	t.Helper()
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer unix.Close(fd)
	identity, err := IdentifyDescriptor(fd)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
