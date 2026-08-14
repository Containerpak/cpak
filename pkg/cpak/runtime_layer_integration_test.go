//go:build integration

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

func TestRuntimeLayerInstallsDebWithDpkg(t *testing.T) {
	previousVerbose := isVerbose
	isVerbose = true
	defer func() { isVerbose = previousVerbose }()

	binary := os.Getenv("CPAK_TEST_BINARY")
	if binary == "" {
		t.Fatal("CPAK_TEST_BINARY is required")
	}
	originalArg := os.Args[0]
	os.Args[0] = binary
	defer func() { os.Args[0] = originalArg }()

	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CPAK_INSTALLATION_PATH", filepath.Join(root, "cpak"))
	cp, err := NewCpak()
	if err != nil {
		t.Fatal(err)
	}

	manifest := &types.CpakManifest{
		Name:        "runtime-probe",
		Description: "Runtime package installation probe",
		Image:       "ghcr.io/containerpak/cpak-test:main",
		Binaries:    []string{"/usr/bin/hello"},
		RuntimeSources: []types.RuntimeSource{{
			Name:      "hello.deb",
			URL:       "https://archive.ubuntu.com/ubuntu/pool/main/h/hello/hello_2.10-5build1_amd64.deb",
			SHA256:    "b6e9bef3c865aa17757cd9ffa820c05a37833f8a6b58287afc9f32dedb345965",
			Size:      26068,
			Installer: "dpkg",
		}},
	}
	if err = cp.InstallCpak("github.com/containerpak/runtime-probe", manifest, "main", "", ""); err != nil {
		t.Fatal(err)
	}
	app, err := cp.getStoredApplication("github.com/containerpak/runtime-probe", "main", "main", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(app.ParsedLayers) < 2 {
		t.Fatalf("runtime layer missing: %v", app.ParsedLayers)
	}
	runtimeLayer := cp.GetInStoreDir("layers", app.ParsedLayers[len(app.ParsedLayers)-1])
	if _, err = os.Stat(filepath.Join(runtimeLayer, "usr", "bin", "hello")); err != nil {
		t.Fatalf("dpkg did not install hello in the runtime layer: %v", err)
	}
}
