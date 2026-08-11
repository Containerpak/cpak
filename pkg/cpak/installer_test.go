/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
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

func TestExportBinaryForwardsFlagArguments(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{Origin: "github.com/containerpak/umu"}
	if err := c.exportBinary(app, "/usr/local/bin/umu-run"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(
		c.Options.ExportsPath,
		"github.com",
		"containerpak",
		"umu",
		"umu-run",
	)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "@/usr/local/bin/umu-run -- \"$@\"") {
		t.Fatalf("export does not preserve child flags: %q", content)
	}
}
