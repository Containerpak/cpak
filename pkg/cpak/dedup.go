/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

func storageMetadataDirectory(name string) bool {
	return name == ".fvs2" || name == ".objects" || name == ".dabadee-v2"
}

// DeduplicatePath shares file content without replacing storage metadata.
func (c *Cpak) DeduplicatePath(root string) (store.TreeResult, error) {
	result := store.TreeResult{}
	root, err := filepath.Abs(root)
	if err != nil {
		return result, err
	}
	for _, part := range strings.Split(root, string(filepath.Separator)) {
		if storageMetadataDirectory(part) {
			return result, fmt.Errorf("dedup: storage metadata cannot be deduplicated: %s", root)
		}
	}
	s, err := store.Open(c.daBaDeeStoreOptions())
	if err != nil {
		return result, err
	}
	defer s.Close()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := c.Ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if storageMetadataDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return err
		}
		metadata := store.MetadataFromInfo(info)
		imported, err := s.ImportFile(c.Ctx, path, path, store.ImportOptions{Metadata: &metadata})
		if err != nil {
			return err
		}
		result.Files++
		result.Written += imported.Written
		result.Reused += imported.Reused
		return nil
	})
	return result, err
}
