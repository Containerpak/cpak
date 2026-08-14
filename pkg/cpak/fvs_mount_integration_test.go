//go:build linux

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestLegacyRunningContainerDoesNotRequireAnFVSMount(t *testing.T) {
	cp := newTestCpak(t)
	if !cp.containerLayerMountAlive(types.Container{}) {
		t.Fatal("legacy container was treated as a failed FVS mount")
	}
	if cp.containerLayerMountAlive(types.Container{FVSLayerMountId: "missing", FVSLayerMountPath: "/missing"}) {
		t.Fatal("missing FVS mount was treated as alive")
	}
}

func TestFVSManagerSocketIsScopedToTheMountNamespace(t *testing.T) {
	cp := newTestCpak(t)
	one, err := cp.fvsManagerSocketForNamespace("mnt:[1]")
	if err != nil {
		t.Fatal(err)
	}
	two, err := cp.fvsManagerSocketForNamespace("mnt:[2]")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := cp.legacyFVSManagerSocket()
	if err != nil {
		t.Fatal(err)
	}
	if one == two || one == legacy || two == legacy {
		t.Fatalf("storage service sockets are not isolated: %q %q %q", one, two, legacy)
	}
}

func TestFVSMountLifecycle(t *testing.T) {
	binary := os.Getenv("CPAK_FVS2D_TEST_BINARY")
	if binary == "" {
		t.Skip("CPAK_FVS2D_TEST_BINARY is not set")
	}
	t.Setenv("CPAK_FVS2D_BINARY", binary)
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, "base", "usr/share/base", []byte("base"))
	seedFVSLayerFile(t, cp, "top", "usr/share/top", []byte("top"))
	state := cp.GetInStoreDir("states", "test")
	mountID, mountPath, managerSocket, err := cp.prepareFVSMount(state, []string{"base", "top"})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.releaseFVSMount(mountID, managerSocket)
	if !cp.fvsMountAlive(mountID, mountPath, managerSocket) {
		t.Fatalf("storage backend did not return a mounted view: %s", mountPath)
	}
	for name, expected := range map[string]string{"base": "base", "top": "top"} {
		content, err := os.ReadFile(filepath.Join(mountPath, "usr", "share", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != expected {
			t.Fatalf("%s = %q", name, content)
		}
	}
	if err := cp.releaseFVSMount(mountID, managerSocket); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mountPath); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredStorageBackend(t *testing.T) {
	tests := map[string]storageBackend{
		"":          storageBackendAuto,
		"auto":      storageBackendAuto,
		"native":    storageBackendNative,
		"overlayfs": storageBackendNative,
		"fuse":      storageBackendFUSE,
	}
	for value, expected := range tests {
		t.Run(value, func(t *testing.T) {
			t.Setenv("CPAK_STORAGE_BACKEND", value)
			backend, err := configuredStorageBackend()
			if err != nil || backend != expected {
				t.Fatalf("backend = %s, err = %v, want %s", backend, err, expected)
			}
		})
	}
	t.Setenv("CPAK_STORAGE_BACKEND", "invalid")
	if _, err := configuredStorageBackend(); err == nil {
		t.Fatal("invalid storage backend was accepted")
	}
}

func TestFVSMountMigratesLegacyLayer(t *testing.T) {
	binary := os.Getenv("CPAK_FVS2D_TEST_BINARY")
	if binary == "" {
		t.Skip("CPAK_FVS2D_TEST_BINARY is not set")
	}
	t.Setenv("CPAK_FVS2D_BINARY", binary)
	cp := newTestCpak(t)
	legacy := cp.GetInStoreDir("layers", "legacy")
	if err := os.MkdirAll(filepath.Join(legacy, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "usr", "share", "value"), []byte("migrated"), 0o640); err != nil {
		t.Fatal(err)
	}

	state := cp.GetInStoreDir("states", "migration")
	mountID, mountPath, managerSocket, err := cp.prepareFVSMount(state, []string{"legacy"})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.cleanupFVSMount(mountID, mountPath, managerSocket)

	content, err := os.ReadFile(filepath.Join(mountPath, "usr", "share", "value"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "migrated" {
		t.Fatalf("content = %q", content)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy layer still present: %v", err)
	}
}

func TestFVSUpgradeFromV201Store(t *testing.T) {
	binary := os.Getenv("CPAK_FVS2D_TEST_BINARY")
	fixture := os.Getenv("CPAK_V201_STORE_FIXTURE")
	if binary == "" || fixture == "" {
		t.Skip("CPAK_FVS2D_TEST_BINARY and CPAK_V201_STORE_FIXTURE are required")
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CPAK_INSTALLATION_PATH", root)
	t.Setenv("CPAK_FVS2D_BINARY", binary)
	cp, err := NewCpak()
	if err != nil {
		t.Fatal(err)
	}
	apps, err := cp.GetInstalledApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 54 {
		t.Fatalf("applications = %d", len(apps))
	}

	legacy := cp.GetInStoreDir("layers", "upgrade-probe")
	if err := os.MkdirAll(filepath.Join(legacy, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "usr", "share", "version"), []byte("2.0.1"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := cp.GetInStoreDir("states", "upgrade-probe")
	mountID, mountPath, managerSocket, err := cp.prepareFVSMount(state, []string{"upgrade-probe"})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.cleanupFVSMount(mountID, mountPath, managerSocket)
	content, err := os.ReadFile(filepath.Join(mountPath, "usr", "share", "version"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "2.0.1" {
		t.Fatalf("content = %q", content)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy layer still present: %v", err)
	}
	apps, err = cp.GetInstalledApps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 54 {
		t.Fatalf("applications after migration = %d", len(apps))
	}
}
