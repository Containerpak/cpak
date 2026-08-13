/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
)

func TestUseIsolatedCpakEnvironmentRestoresProcessState(t *testing.T) {
	const original = "/tmp/original-cpak-test-path"
	t.Setenv("CPAK_INSTALLATION_PATH", original)
	cleanup, err := useIsolatedCpakEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	root := os.Getenv("CPAK_INSTALLATION_PATH")
	if root == "" || root == original || !filepath.IsAbs(root) {
		t.Fatalf("unexpected isolated path: %q", root)
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		t.Fatal(err)
	}
	if cp.Options.StorePath != filepath.Join(root, "store") || cp.Options.CachePath != filepath.Join(root, "cache") {
		t.Fatalf("isolated options were not applied: %+v", cp.Options)
	}
	if err = cleanup(); err != nil {
		t.Fatal(err)
	}
	if actual := os.Getenv("CPAK_INSTALLATION_PATH"); actual != original {
		t.Fatalf("environment was not restored: %s", actual)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("isolated path was not removed: %v", err)
	}
}
