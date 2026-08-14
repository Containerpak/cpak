/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// IsMounted checks if the given source path is mounted in the given
// destination path. It does so by reading the /proc/mounts file.
func IsMounted(srcPath string, destPath string) (bool, error) {
	mounts, err := os.Open("/proc/mounts")
	if err != nil {
		return false, fmt.Errorf("error opening /proc/mounts: %w", err)
	}
	defer mounts.Close()

	scanner := bufio.NewScanner(mounts)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, srcPath) && strings.Contains(line, destPath) {
			return true, nil
		}
	}

	return false, nil
}

// Mount mounts the given source path in the given destination path, by
// calling the mount syscall.
func Mount(src, dest string, mode uintptr) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err = os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		destInfo, statErr := os.Lstat(dest)
		if statErr == nil && destInfo.Mode()&os.ModeSymlink != 0 {
			if removeErr := os.Remove(dest); removeErr != nil {
				return removeErr
			}
			statErr = os.ErrNotExist
		}
		if os.IsNotExist(statErr) {
			file, createErr := os.Create(dest)
			if createErr != nil {
				return createErr
			}
			if closeErr := file.Close(); closeErr != nil {
				return closeErr
			}
		} else if statErr != nil {
			return statErr
		}
	}

	return MountPrepared(src, dest, mode)
}

// MountPrepared mounts an existing, non-symlink destination. Callers that
// prepare a target under an image root should use this after PrepareRootfsTarget.
func MountPrepared(src, dest string, mode uintptr) error {
	source, err := os.Stat(src)
	if err != nil {
		return err
	}
	destination, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if destination.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("mount destination is a symlink: %s", dest)
	}
	if source.IsDir() != destination.IsDir() || !source.IsDir() && !destination.Mode().IsRegular() {
		return fmt.Errorf("mount destination type does not match source: %s", dest)
	}
	return syscall.Mount(src, dest, "bind", mode, "")
}

// MountBind creates a recursive private bind mount.
func MountBind(src, dest string) error {
	if err := Mount(src, dest, syscall.MS_BIND|syscall.MS_REC); err != nil {
		return err
	}
	return syscall.Mount("", dest, "", syscall.MS_PRIVATE|syscall.MS_REC, "")
}

// MountBindPrepared creates a recursive private bind mount at a prepared
// destination.
func MountBindPrepared(src, dest string) error {
	if err := MountPrepared(src, dest, syscall.MS_BIND|syscall.MS_REC); err != nil {
		return err
	}
	return syscall.Mount("", dest, "", syscall.MS_PRIVATE|syscall.MS_REC, "")
}

func IsSameFile(first, second string) bool {
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

// MountBindReadOnly creates a recursive bind mount and applies read-only
// restrictions after the bind, since the initial bind ignores these flags.
func MountBindReadOnly(src, dest string, noExec bool) error {
	if err := MountBind(src, dest); err != nil {
		return fmt.Errorf("bind %s to %s: %w", src, dest, err)
	}
	return restrictBindMount(dest, noExec)
}

// MountBindReadOnlyPrepared creates a restricted bind mount at a prepared
// destination.
func MountBindReadOnlyPrepared(src, dest string, noExec bool) error {
	if err := MountBindPrepared(src, dest); err != nil {
		return fmt.Errorf("bind %s to %s: %w", src, dest, err)
	}
	return restrictBindMount(dest, noExec)
}

func restrictBindMount(dest string, noExec bool) error {

	attributes := uint64(unix.MOUNT_ATTR_RDONLY | unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV)
	if noExec {
		attributes |= unix.MOUNT_ATTR_NOEXEC
	}
	err := unix.MountSetattr(unix.AT_FDCWD, dest, unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: attributes})
	if err == nil {
		return nil
	}
	if err != unix.ENOSYS && err != unix.EINVAL && err != unix.EOPNOTSUPP {
		return fmt.Errorf("restrict bind mount %s: %w", dest, err)
	}

	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
	if noExec {
		flags |= syscall.MS_NOEXEC
	}
	if err = syscall.Mount("", dest, "", flags, ""); err != nil {
		return fmt.Errorf("remount %s read-only: %w", dest, err)
	}
	return nil
}

// MountOverlay mounts the given lower, upper and work directories in the
// given destination path as an overlay filesystem.
func MountOverlay(targetDir, lowerDir, upperDir, workDir string) error {
	return syscall.Mount(
		"overlay", targetDir, "overlay", 0,
		fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s,userxattr", lowerDir, upperDir, workDir),
	)
}

func MountFuseOverlayfs(targetDir, lowerDir, upperDir, workDir string) (err error) {
	c := exec.Command("fuse-overlayfs", targetDir, "-o", fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerDir, upperDir, workDir))
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func MountTmpfs(targetDir string) (err error) {
	if err = os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}
	return syscall.Mount("tmpfs", targetDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755")
}

// MountTmpfsPrepared mounts tmpfs on an existing, non-symlink directory.
func MountTmpfsPrepared(targetDir string) error {
	info, err := os.Lstat(targetDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("tmpfs destination must be a directory: %s", targetDir)
	}
	return syscall.Mount("tmpfs", targetDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=0755")
}

func MountDevptsPrepared(targetDir string) error {
	info, err := os.Lstat(targetDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("devpts destination must be a directory: %s", targetDir)
	}
	return syscall.Mount(
		"devpts",
		targetDir,
		"devpts",
		syscall.MS_NOSUID|syscall.MS_NOEXEC,
		"newinstance,ptmxmode=0666,mode=0620,gid=0",
	)
}

func GetHostMounts() (mounts []string) {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		mounts = append(mounts, fields[1])
	}

	return
}
