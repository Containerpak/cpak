/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package filegrant

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func OpenSource(grant Grant) (*os.File, error) {
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(grant.Source)
	if err != nil {
		return nil, fmt.Errorf("resolve file grant source: %w", err)
	}
	if resolved != grant.Source {
		return nil, errors.New("file grant source changed after selection")
	}
	flags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if grant.Kind == KindDirectory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(grant.Source, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open file grant source: %w", err)
	}
	file := os.NewFile(uintptr(fd), grant.Source)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open file grant source")
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect file grant descriptor: %w", err)
	}
	current, err := os.Stat(grant.Source)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect file grant source: %w", err)
	}
	if !os.SameFile(opened, current) {
		_ = file.Close()
		return nil, errors.New("file grant source changed while opening")
	}
	if grant.Kind == KindDirectory && !opened.IsDir() || grant.Kind == KindFile && !opened.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("file grant source type changed after selection")
	}
	return file, nil
}

func OpenMountSource(grant Grant) (*os.File, error) {
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	if grant.Kind != KindFile {
		return nil, nil
	}
	parent := filepath.Dir(grant.Source)
	fd, err := unix.Open(parent, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open file grant parent: %w", err)
	}
	directory := os.NewFile(uintptr(fd), parent)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open file grant parent")
	}
	return directory, nil
}
