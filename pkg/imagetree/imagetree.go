/*
 * Copyright (c) 2026 Marcin Jahn
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package imagetree removes directory trees materialized from image layers.
//
// A checkout keeps the directory modes the image recorded, and images such as
// Fedora ship /usr/bin, /usr/lib or /root as 0555. The tree belongs to the
// user who checked it out, but without write permission on those directories
// that user cannot unlink what they contain, so a plain os.RemoveAll fails as
// soon as it runs without root.
package imagetree

import (
	"errors"
	"io/fs"
	"os"
)

const ownerAccess fs.FileMode = 0o700

// RemoveAll removes path and everything it contains, as os.RemoveAll does.
// When that is refused, it gives the owner full access to every directory in
// the tree and removes it again. Symbolic links are never followed, so nothing
// outside path changes mode.
func RemoveAll(path string) error {
	err := os.RemoveAll(path)
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if grantErr := grantOwnerAccess(path); grantErr != nil {
		return errors.Join(err, grantErr)
	}
	return os.RemoveAll(path)
}

func grantOwnerAccess(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	// WalkDir visits a directory before it reads it, so a directory the owner
	// cannot list is opened up in time to be walked.
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().Perm()&ownerAccess == ownerAccess {
			return nil
		}
		return root.Chmod(name, info.Mode().Perm()|ownerAccess)
	})
}
