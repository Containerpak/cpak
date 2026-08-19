/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A package names the desktop entries it exports, and cpak writes each of them
// twice into ~/.local/share/applications: once under a name derived from the
// installation, and once under the name the package chose, so that anything
// already referring to the application by that name keeps finding it.
//
// The second copy is the package's own name in the user's directory, and that
// directory holds more than launchers. mimeapps.list is the default handler
// database, and a package declaring usr/share/applications/mimeapps.list wrote
// it: not as an override of the user's configuration but as the file itself,
// first writer wins, after which GIO resolved text/plain and
// x-scheme-handler/http to whatever the package had put there. defaults.list
// and mimeinfo.cache are read the same way.
//
// The schema says the same thing (see manifestItemPatterns), and says it to a
// manifest. This says it to a name, which is what the export has: the export
// runs over what the store recorded at install time, not over a manifest that
// has just been validated.

// desktopEntrySuffix is what a launcher is called. Nothing else in an
// applications directory is one.
const desktopEntrySuffix = ".desktop"

// desktopHandlerFiles are the files in an applications directory that decide
// what opens a document rather than describing something to open it with.
//
// None of them ends in .desktop, so the suffix rule refuses them already. They
// are named because a name refused by accident stays refused only until the
// rule around it moves, and these three are what the finding was about.
var desktopHandlerFiles = map[string]bool{
	"mimeapps.list":  true,
	"defaults.list":  true,
	"mimeinfo.cache": true,
}

// desktopEntryExportName answers the file name a declared desktop entry is
// exported under, and refuses a declaration cpak will not write. The name is
// the package's and the directory is the user's, so the name is all there is
// to check.
func desktopEntryExportName(entry string) (string, error) {
	name, err := singlePathComponent(filepath.Base(entry))
	if err != nil {
		return "", fmt.Errorf("invalid desktop entry: %q", entry)
	}
	if !strings.HasSuffix(name, desktopEntrySuffix) || name == desktopEntrySuffix {
		return "", fmt.Errorf("desktop entry %q is not a %s file", entry, desktopEntrySuffix)
	}
	if desktopHandlerFiles[name] {
		return "", fmt.Errorf("desktop entry %q is a handler database, not a launcher", entry)
	}
	return name, nil
}
