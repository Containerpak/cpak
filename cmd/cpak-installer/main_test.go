/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
