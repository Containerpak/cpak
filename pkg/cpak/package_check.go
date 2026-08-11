/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// ValidateApplicationFiles checks that every declared export exists.
func (c *Cpak) ValidateApplicationFiles(app types.Application) error {
	for _, binary := range app.ParsedBinaries {
		if !c.applicationPathExists(app, binary, true) {
			return fmt.Errorf("binary %s is missing from the installed layers", binary)
		}
	}
	for _, desktopEntry := range app.ParsedDesktopEntries {
		if !c.applicationPathExists(app, desktopEntry, false) {
			return fmt.Errorf("desktop entry %s is missing from the installed layers", desktopEntry)
		}
	}
	return nil
}

func (c *Cpak) applicationPathExists(app types.Application, path string, executable bool) bool {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path || clean == string(filepath.Separator) {
		return false
	}
	path = strings.TrimPrefix(clean, string(filepath.Separator))
	for index := len(app.ParsedLayers) - 1; index >= 0; index-- {
		candidate := c.GetInStoreDir("layers", app.ParsedLayers[index], path)
		info, err := os.Lstat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if executable && info.Mode().IsRegular() && info.Mode().Perm()&0111 == 0 {
			return false
		}
		return true
	}
	return false
}
