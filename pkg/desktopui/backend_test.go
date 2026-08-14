/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelectBackendFallsBackToBuiltin(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if backend := SelectBackend(BackendAuto); backend != BackendBuiltin {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestSelectBackendUsesDesktopTool(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "zenity")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if backend := SelectBackend(BackendAuto); backend != BackendGNOME {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestConfiguredBackendFallsBackWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if backend := SelectBackend(BackendKDE); backend != BackendBuiltin {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestBuildDefaultSelectsAnAvailableBackend(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "kdialog")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	previous := defaultBackend
	defaultBackend = "kde"
	t.Cleanup(func() { defaultBackend = previous })
	if backend := SelectBackend(""); backend != BackendKDE {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestConfigOverridesTheBuildDefault(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "zenity")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "cpak.json")
	if err := os.WriteFile(config, []byte(`{"desktop":{"dialog_backend":"gnome"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("CPAK_OPTS_FILE", config)
	previous := defaultBackend
	defaultBackend = "kde"
	t.Cleanup(func() { defaultBackend = previous })
	if backend := SelectBackend(""); backend != BackendGNOME {
		t.Fatalf("unexpected backend: %s", backend)
	}
}
