/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"path"
	"strings"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// ValidateApplicationFiles checks that every declared export exists.
func (c *Cpak) ValidateApplicationFiles(app types.Application) error {
	if len(app.ParsedBinaries) == 0 && len(app.ParsedDesktopEntries) == 0 {
		return nil
	}
	entries, err := c.fvsMergedEntries(app.ParsedLayers)
	if err != nil {
		return err
	}
	for _, binary := range app.ParsedBinaries {
		if !fvsApplicationPathExists(entries, binary, true) {
			return fmt.Errorf("binary %s is missing from the installed layers", binary)
		}
	}
	for _, desktopEntry := range app.ParsedDesktopEntries {
		if !fvsApplicationPathExists(entries, desktopEntry, false) {
			return fmt.Errorf("desktop entry %s is missing from the installed layers", desktopEntry)
		}
	}
	return nil
}

func fvsApplicationPathExists(entries map[string]fvsViewEntry, name string, executable bool) bool {
	clean, ok := applicationEntryName(name)
	if !ok {
		return false
	}
	entry, ok := entries[clean]
	if !ok || entry.file.Kind == string(fvsrepo.EntryDir) {
		return false
	}
	return !executable || entry.file.Kind != "" && entry.file.Kind != string(fvsrepo.EntryFile) || entry.file.Mode&0o111 != 0
}

func (c *Cpak) applicationPathExists(app types.Application, path string, executable bool) bool {
	entries, err := c.fvsMergedEntries(app.ParsedLayers)
	return err == nil && fvsApplicationPathExists(entries, path, executable)
}

func applicationEntryName(name string) (string, bool) {
	if !strings.HasPrefix(name, "/") {
		return "", false
	}
	clean := path.Clean(name)
	if clean != name || clean == "/" {
		return "", false
	}
	return strings.TrimPrefix(clean, "/"), true
}
