/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestValidateApplicationFilesChecksEveryExport(t *testing.T) {
	cp := newTestCpak(t)
	layer := cp.GetInStoreDir("layers", "layer")
	if err := os.MkdirAll(filepath.Join(layer, "usr/bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "usr/bin/demo"), []byte("demo"), 0755); err != nil {
		t.Fatal(err)
	}
	app := types.Application{ParsedLayers: []string{"layer"}, ParsedBinaries: []string{"/usr/bin/demo"}}
	if err := cp.ValidateApplicationFiles(app); err != nil {
		t.Fatal(err)
	}
	app.ParsedDesktopEntries = []string{"/usr/share/applications/demo.desktop"}
	if err := cp.ValidateApplicationFiles(app); err == nil {
		t.Fatal("missing desktop entry was accepted")
	}
}

func TestValidateApplicationFilesRejectsDirectoriesAndNonExecutables(t *testing.T) {
	cp := newTestCpak(t)
	layer := cp.GetInStoreDir("layers", "layer", "usr", "bin")
	if err := os.MkdirAll(filepath.Join(layer, "directory"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "not-executable"), []byte("demo"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, binary := range []string{"/usr/bin/directory", "/usr/bin/not-executable", "/usr/bin/../bin/not-executable"} {
		app := types.Application{ParsedLayers: []string{"layer"}, ParsedBinaries: []string{binary}}
		if err := cp.ValidateApplicationFiles(app); err == nil {
			t.Fatalf("invalid binary was accepted: %s", binary)
		}
	}
}
