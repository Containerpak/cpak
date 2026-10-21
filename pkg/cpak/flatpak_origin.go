/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"strings"
)

// flatpakOriginHost is the first element of every origin a Flatpak
// application is installed under. A Git origin needs a host with a dot in it
// or an address, so this one can never name a repository, and nothing that
// fetches from Git can be pointed at a Flatpak installation by accident.
const flatpakOriginHost = "flatpak"

var ErrFlatpakOrigin = errors.New("this is a Flatpak installation and has no Git repository")

// FlatpakOrigin answers with the origin an application from a remote is
// installed under. Origins are lowercase, so the id is too; the exact id is
// kept in the installation record.
func FlatpakOrigin(remote, id string) string {
	return flatpakOriginHost + "/" + remote + "/" + strings.ToLower(id)
}

// IsFlatpakOrigin reports whether an origin names a Flatpak installation.
func IsFlatpakOrigin(origin string) bool {
	return strings.HasPrefix(origin, flatpakOriginHost+"/")
}

func refuseFlatpakOrigin(origin string) error {
	if IsFlatpakOrigin(origin) {
		return fmt.Errorf("%s: %w; install it with --flatpak", origin, ErrFlatpakOrigin)
	}
	return nil
}
