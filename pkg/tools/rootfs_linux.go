/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type RootfsTargetKind uint8

const (
	RootfsTargetDirectory RootfsTargetKind = iota
	RootfsTargetFile
)

// PrepareRootfsTarget creates a mount target below rootfs without following
// symlinks from the image. target must be a non-root guest path without dot
// components.
func PrepareRootfsTarget(rootfs, target string, kind RootfsTargetKind) (string, error) {
	parts, err := rootfsTargetParts(target)
	if err != nil {
		return "", err
	}

	rootfd, err := unix.Open(rootfs, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open rootfs: %w", err)
	}
	defer unix.Close(rootfd)

	parentfd := rootfd
	for _, part := range parts[:len(parts)-1] {
		nextfd, openErr := openOrCreateDirectory(parentfd, part)
		if openErr != nil {
			if parentfd != rootfd {
				_ = unix.Close(parentfd)
			}
			return "", openErr
		}
		if parentfd != rootfd {
			_ = unix.Close(parentfd)
		}
		parentfd = nextfd
	}
	if parentfd != rootfd {
		defer unix.Close(parentfd)
	}

	if kind == RootfsTargetDirectory {
		fd, openErr := openOrCreateDirectory(parentfd, parts[len(parts)-1])
		if openErr != nil {
			return "", openErr
		}
		if err = unix.Close(fd); err != nil {
			return "", fmt.Errorf("close rootfs directory target: %w", err)
		}
		return filepath.Join(rootfs, filepath.FromSlash(strings.Join(parts, "/"))), nil
	}
	if err = openOrCreateFile(parentfd, parts[len(parts)-1]); err != nil {
		return "", err
	}
	return filepath.Join(rootfs, filepath.FromSlash(strings.Join(parts, "/"))), nil
}

func rootfsTargetParts(target string) ([]string, error) {
	if target == "" || !filepath.IsAbs(target) {
		return nil, fmt.Errorf("rootfs target must be an absolute path: %q", target)
	}
	for _, part := range strings.Split(target, "/") {
		if part == "." || part == ".." {
			return nil, fmt.Errorf("rootfs target contains an invalid path component: %q", target)
		}
	}
	clean := filepath.Clean(target)
	if clean == "/" {
		return nil, fmt.Errorf("rootfs target must not be the root directory: %q", target)
	}
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	return parts, nil
}

func openOrCreateDirectory(parentfd int, name string) (int, error) {
	fd, err := unix.Openat(parentfd, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		if err = unix.Mkdirat(parentfd, name, 0o755); err != nil && err != unix.EEXIST {
			return -1, fmt.Errorf("create rootfs directory %s: %w", name, err)
		}
		fd, err = unix.Openat(parentfd, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return -1, fmt.Errorf("open rootfs directory %s: %w", name, err)
	}
	var stat unix.Stat_t
	if statErr := unix.Fstat(fd, &stat); statErr != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("stat rootfs directory %s: %w", name, statErr)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("rootfs directory target %s has the wrong type", name)
	}
	return fd, nil
}

func openOrCreateFile(parentfd int, name string) error {
	fd, err := unix.Openat(parentfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		fd, err = unix.Openat(parentfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	}
	if err != nil {
		return fmt.Errorf("open rootfs file %s: %w", name, err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat rootfs file %s: %w", name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("rootfs file target %s has the wrong type", name)
	}
	return nil
}
