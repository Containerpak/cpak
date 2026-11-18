/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// SetWorkingDirectory selects an existing host directory without granting it.
func (c *Cpak) SetWorkingDirectory(directory string) error {
	if directory == "" {
		c.workingDirectory = ""
		return nil
	}
	resolved, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	c.workingDirectory = resolved
	return nil
}

func (c *Cpak) runtimeWorkingDirectory(override types.Override) (string, error) {
	directory := c.workingDirectory
	explicit := directory != ""
	if !explicit {
		if c.desktopLaunch || c.applicationService != "" {
			return "", nil
		}
		directory, _ = os.Getwd()
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		if explicit {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		return "", nil
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		if explicit {
			return "", fmt.Errorf("working directory is not a directory: %s", directory)
		}
		return "", nil
	}
	selected, matched := "", -1
	if !types.PathIsCpakState(resolved, c.cpakStateDirectories()) {
		for _, permission := range override.WithMigratedFilesystem().Filesystem {
			source, target, err := types.ResolveFilesystemPermission(permission)
			if err != nil {
				continue
			}
			source, err = filepath.EvalSymlinks(source)
			if err != nil || len(source) <= matched {
				continue
			}
			relative, err := filepath.Rel(source, resolved)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			selected = filepath.Join(target, relative)
			matched = len(source)
		}
	}
	if selected == "" && explicit {
		return "", fmt.Errorf("working directory is not shared with this application: %s", directory)
	}
	return selected, nil
}
