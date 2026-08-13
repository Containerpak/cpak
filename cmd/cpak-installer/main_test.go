/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCpakIsAtomicAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := []byte("cpak binary")
	path, changed, err := installCpak(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || path != filepath.Join(home, ".local", "bin", "cpak") {
		t.Fatalf("unexpected install result: %s %v", path, changed)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0755 {
		t.Fatalf("unexpected mode: %v", stat.Mode())
	}
	_, changed, err = installCpak(payload)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unchanged payload was reinstalled")
	}
}

func TestGUIProgressLabelHidesCommandOutput(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "cpak is ready", want: "cpak is ready"},
		{message: "Installed cpak in ~/.local/bin", want: "cpak is ready"},
		{message: "Resolving Bottles", want: "Preparing Bottles"},
		{message: "Downloading sha256:abc", want: "Downloading Bottles"},
		{message: "Extracting layer", want: "Installing Bottles"},
		{message: "Resolved commit f2700afd2980dda29a73284f8b182e32c2071d5cb4fc9b7ac72579641b3cbb", want: ""},
	}
	for _, test := range tests {
		if got := guiProgressLabel(test.message, "Bottles"); got != test.want {
			t.Fatalf("guiProgressLabel(%q) = %q, want %q", test.message, got, test.want)
		}
	}
}
