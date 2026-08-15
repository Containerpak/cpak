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
	withoutEmbeddedAdapters(t)
	t.Setenv("CPAK_UI_ADAPTER_DIR", t.TempDir())
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if backend := SelectBackend(BackendAuto); backend != BackendBuiltin {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestSelectBackendUsesDesktopAdapter(t *testing.T) {
	if !adapterBuilt(BackendAdwaita) {
		t.Skip("adwaita adapter is not part of this build")
	}
	directory := t.TempDir()
	withoutEmbeddedAdapters(t)
	writeTestAdapter(t, directory, BackendAdwaita)
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	t.Setenv("XDG_CURRENT_DESKTOP", "GNOME")
	if backend := SelectBackend(BackendAuto); backend != BackendAdwaita {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestConfiguredBackendFallsBackWhenUnavailable(t *testing.T) {
	withoutEmbeddedAdapters(t)
	t.Setenv("CPAK_UI_ADAPTER_DIR", t.TempDir())
	if backend := SelectBackend(BackendKDE); backend != BackendBuiltin {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestBuildDefaultSelectsAnAvailableBackend(t *testing.T) {
	if !adapterBuilt(BackendKDE) {
		t.Skip("kde adapter is not part of this build")
	}
	directory := t.TempDir()
	withoutEmbeddedAdapters(t)
	writeTestAdapter(t, directory, BackendKDE)
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	previous := defaultBackend
	defaultBackend = "kde"
	t.Cleanup(func() { defaultBackend = previous })
	if backend := SelectBackend(""); backend != BackendKDE {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestConfigOverridesTheBuildDefault(t *testing.T) {
	if !adapterBuilt(BackendGTK) {
		t.Skip("gtk adapter is not part of this build")
	}
	directory := t.TempDir()
	withoutEmbeddedAdapters(t)
	writeTestAdapter(t, directory, BackendGTK)
	config := filepath.Join(directory, "cpak.json")
	if err := os.WriteFile(config, []byte(`{"desktop":{"dialog_backend":"gtk"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	t.Setenv("CPAK_OPTS_FILE", config)
	previous := defaultBackend
	defaultBackend = "kde"
	t.Cleanup(func() { defaultBackend = previous })
	if backend := SelectBackend(""); backend != BackendGTK {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestEnvironmentOverridesConfigAndBuildDefault(t *testing.T) {
	if !adapterBuilt(BackendQt) {
		t.Skip("qt adapter is not part of this build")
	}
	directory := t.TempDir()
	withoutEmbeddedAdapters(t)
	writeTestAdapter(t, directory, BackendQt)
	config := filepath.Join(directory, "cpak.json")
	if err := os.WriteFile(config, []byte(`{"desktop":{"dialog_backend":"gtk"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	t.Setenv("CPAK_OPTS_FILE", config)
	t.Setenv("CPAK_UI_ADAPTER", "qt")
	if backend := SelectBackend(""); backend != BackendQt {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func TestPlasmaFallsBackToQt(t *testing.T) {
	if !adapterBuilt(BackendQt) {
		t.Skip("qt adapter is not part of this build")
	}
	directory := t.TempDir()
	withoutEmbeddedAdapters(t)
	writeTestAdapter(t, directory, BackendQt)
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	t.Setenv("XDG_CURRENT_DESKTOP", "KDE")
	if backend := SelectBackend(BackendAuto); backend != BackendQt {
		t.Fatalf("unexpected backend: %s", backend)
	}
}

func writeTestAdapter(t *testing.T, directory string, backend Backend) {
	t.Helper()
	path := filepath.Join(directory, "cpak-ui-"+string(backend))
	content := "#!/bin/sh\nprintf 'cpak-ui 1 " + string(backend) + "\\n'\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func withoutEmbeddedAdapters(t *testing.T) {
	t.Helper()
	previous := embeddedAdapters
	embeddedAdapters = map[Backend][]byte{}
	t.Cleanup(func() { embeddedAdapters = previous })
}
