/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"strings"
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

func TestUseIsolatedCpakEnvironmentWithLongTemporaryPath(t *testing.T) {
	base := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", base)
	cleanup, err := useIsolatedCpakEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	root := os.Getenv("CPAK_INSTALLATION_PATH")
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatalf("isolated path is not absolute and clean: %q", root)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("isolated directory is not private: %v, %v", info, err)
	}
	path, err := cpak.HostServiceSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := (&SpawnCmd{ExecSocket: path}).createRuntimeListener()
	if err != nil {
		t.Fatalf("isolated service cannot listen at %d bytes: %v", len(path), err)
	}
	listener.Close()
	state := filepath.Join(root, "store", "states", strings.Repeat("0", 36))
	if err = os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "system-broker-v5.sock"), filepath.Join(state, "bluetooth-bus.sock")} {
		listener, err = (&SpawnCmd{ExecSocket: path}).createRuntimeListener()
		if err != nil {
			t.Fatalf("isolated runtime cannot listen at %d bytes: %v", len(path), err)
		}
		listener.Close()
	}
}
