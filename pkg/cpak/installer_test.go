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
)

func TestFindIconPrefersScalableIcon(t *testing.T) {
	layerDir := t.TempDir()
	pngPath := filepath.Join(layerDir, "usr/share/icons/hicolor/128x128/apps/example.png")
	svgPath := filepath.Join(layerDir, "usr/share/icons/hicolor/scalable/apps/example.svg")
	for _, path := range []string{pngPath, svgPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("icon"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if got := findIcon(layerDir, "example"); got != svgPath {
		t.Fatalf("expected scalable icon %q, got %q", svgPath, got)
	}
}
