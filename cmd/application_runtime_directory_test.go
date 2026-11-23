/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFlatpakApplicationGetsItsRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	if err := prepareApplicationRuntimeDirectory(root, []string{"PATH=/usr/bin", "FLATPAK_ID=org.example.App"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "app", "org.example.App"))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("application runtime directory: %v %v", info, err)
	}
	for _, id := range []string{"..", "../escape", "a/b", "."} {
		if err := prepareApplicationRuntimeDirectory(t.TempDir(), []string{"FLATPAK_ID=" + id}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
