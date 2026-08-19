/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

func MountFileBindPrepared(src, dest string, readOnly, noExec bool) error {
	if err := MountPrepared(src, dest, syscall.MS_BIND); err != nil {
		return err
	}
	if readOnly {
		return restrictBindMount(dest, noExec)
	}
	return RestrictWritableBindMount(dest)
}

func MountDescriptorPrepared(sourceFD int, dest string, readOnly, noExec, recursive bool) error {
	destination, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if destination.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("mount destination is a symlink: %s", dest)
	}
	targetFD, err := unix.Open(dest, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open mount destination %s: %w", dest, err)
	}
	defer unix.Close(targetFD)
	treeFD, err := unix.OpenTree(sourceFD, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if err != nil {
		return fmt.Errorf("clone descriptor mount: %w", err)
	}
	defer unix.Close(treeFD)
	attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV)
	if readOnly {
		attributes |= unix.MOUNT_ATTR_RDONLY
	}
	if noExec {
		attributes |= unix.MOUNT_ATTR_NOEXEC
	}
	flags := uint(unix.AT_EMPTY_PATH)
	if recursive {
		flags |= uint(unix.AT_RECURSIVE)
	}
	if err = unix.MountSetattr(treeFD, "", flags, &unix.MountAttr{Attr_set: attributes}); err != nil {
		return fmt.Errorf("restrict descriptor mount: %w", err)
	}
	if err = unix.MoveMount(treeFD, "", targetFD, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach descriptor mount: %w", err)
	}
	return nil
}

func AttachDescriptorMountPrepared(treeFD int, dest string) error {
	destination, err := os.Lstat(dest)
	if err != nil {
		return err
	}
	if destination.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("mount destination is a symlink: %s", dest)
	}
	targetFD, err := unix.Open(dest, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open mount destination %s: %w", dest, err)
	}
	defer unix.Close(targetFD)
	if err = unix.MoveMount(treeFD, "", targetFD, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach descriptor mount: %w", err)
	}
	return nil
}

// RestrictWritableBindMount removes executable, device and set-user-ID
// semantics from a writable bind mount.
func RestrictWritableBindMount(dest string) error {
	attributes := uint64(unix.MOUNT_ATTR_NOSUID | unix.MOUNT_ATTR_NODEV | unix.MOUNT_ATTR_NOEXEC)
	err := unix.MountSetattr(unix.AT_FDCWD, dest, unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: attributes})
	if err == nil {
		return nil
	}
	if !mountSetattrUnsupported(err) {
		return fmt.Errorf("restrict writable bind mount %s: %w", dest, err)
	}
	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	return restrictMountTree(dest, flags, "restricted")
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
	if !mountSetattrUnsupported(err) {
		return fmt.Errorf("restrict bind mount %s: %w", dest, err)
	}

	flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV)
	if noExec {
		flags |= syscall.MS_NOEXEC
	}
	return restrictMountTree(dest, flags, "read-only")
}

// mountSetattrUnsupported answers whether the kernel could not take the call at
// all, which is the only reason to fall back to a remount. The remount is the
// weaker tool: it changes what MS_ flags can express and nothing else, and it
// has to walk the tree by hand to reach what one mount_setattr reaches on its
// own.
//
// EINVAL is not one of those reasons, and reading it as one is how a mistake on
// cpak's side turns into a silent downgrade. mount_setattr answers EINVAL for a
// path that is not the root of a mount and for attributes it will not take, and
// both of those are cpak asking wrongly rather than a kernel that cannot
// answer. Measured on 6.17: the call succeeds on a mount point, answers EINVAL
// on a plain directory, and the MS_REMOUNT the fallback would have run answers
// EINVAL on that same directory, so nothing was ever recovered by falling back.
// What would be lost is the day cpak asks for an attribute the remount has no
// equivalent for: an older kernel answers EINVAL, the fallback reports success,
// and the restriction is simply not there.
//
// A kernel that does not have the syscall at all answers ENOSYS, and one that
// has it and will not do this to this filesystem answers EOPNOTSUPP. Those are
// the two the fallback exists for.
func mountSetattrUnsupported(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

// restrictMountTree remounts dest and every mount under it.
//
// It is the answer to a mount_setattr the kernel would not take, and the whole
// of what makes that answer honest. The recursive form of the syscall arrived
// in 5.12; before it, and whenever the call is refused, the only tool left is
// MS_REMOUNT, which changes the mount it is handed and nothing below it. Every
// caller here restricts a recursive bind: MountBindReadOnlyPrepared("/", ...)
// carries the whole host under /run/host, so a remount of the top left a
// separate /home, /media or /var writable inside the container, with no error
// and no log line to say so.
//
// A mount that cannot be restricted is now a refused container start rather
// than a quiet hole, and the message names the mount that refused.
func restrictMountTree(dest string, flags uintptr, description string) error {
	targets, err := mountTreeTargets(dest)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err = syscall.Mount("", target, "", flags, ""); err != nil {
			return fmt.Errorf("remount %s %s: %w", target, description, err)
		}
	}
	return nil
}

// mountTreeTargets is dest followed by every mount point beneath it, ancestors
// before descendants. dest leads whether or not it is a mount point itself, so
// a path that never was one is still refused by the remount, the way it was
// before anything walked the tree.
func mountTreeTargets(dest string) ([]string, error) {
	table, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read the mount table: %w", err)
	}
	defer table.Close()
	return mountTreeTargetsFrom(table, dest)
}

func mountTreeTargetsFrom(table io.Reader, dest string) ([]string, error) {
	root := filepath.Clean(dest)
	targets := []string{root}
	seen := map[string]struct{}{root: {}}
	scanner := bufio.NewScanner(table)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		point := filepath.Clean(unescapeMountPoint(fields[4]))
		if !mountPointIsUnder(point, root) {
			continue
		}
		if _, known := seen[point]; known {
			continue
		}
		seen[point] = struct{}{}
		targets = append(targets, point)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the mount table: %w", err)
	}
	// A cleaned path sorts before every path it contains, so this is the order
	// the remounts have to happen in: a parent before the mounts it carries.
	sort.Strings(targets)
	return targets, nil
}

func mountPointIsUnder(point, root string) bool {
	if root == "/" {
		return point != "/"
	}
	return strings.HasPrefix(point, root+"/")
}

// unescapeMountPoint reads back the octal escapes the kernel writes for the
// space, tab, newline and backslash a mount point may contain. A mount named
// with a space is otherwise a path that exists and is never restricted.
func unescapeMountPoint(field string) string {
	if !strings.Contains(field, "\\") {
		return field
	}
	var point strings.Builder
	for index := 0; index < len(field); index++ {
		if field[index] == '\\' && index+3 < len(field) {
			if value, err := strconv.ParseUint(field[index+1:index+4], 8, 8); err == nil {
				point.WriteByte(byte(value))
				index += 3
				continue
			}
		}
		point.WriteByte(field[index])
	}
	return point.String()
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
