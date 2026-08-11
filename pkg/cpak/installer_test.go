/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"regexp"
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

func TestFindIconIgnoresFilesThatAreNotIcons(t *testing.T) {
	layerDir := t.TempDir()
	sourcesPath := filepath.Join(layerDir, "etc/apt/sources.list.d/vscode.sources")
	iconPath := filepath.Join(layerDir, "usr/share/pixmaps/vscode.png")
	for _, path := range []string{sourcesPath, iconPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if got := findIcon(layerDir, "vscode"); got != iconPath {
		t.Fatalf("expected image %q, got %q", iconPath, got)
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

func TestExportDesktopEntryUsesDiscoverableApplicationID(t *testing.T) {
	c := newTestCpak(t)
	layer := "desktop-layer"
	layerDir := c.GetInStoreDir("layers", layer)
	entry := "/usr/share/applications/example.desktop"
	entryPath := filepath.Join(layerDir, strings.TrimLeft(entry, "/"))
	if err := os.MkdirAll(filepath.Dir(entryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("[Desktop Entry]\nName=Example\nExec=/usr/bin/example --test %U\nTryExec=/usr/bin/example\n"), 0644); err != nil {
		t.Fatal(err)
	}

	app := types.Application{
		CpakId:               "unsafe/base64=id",
		Origin:               "github.com/containerpak/example",
		ParsedDesktopEntries: []string{entry},
		ParsedLayers:         []string{layer},
	}
	legacyDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", app.CpakId)
	if err := os.MkdirAll(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	legacyIcon := filepath.Join(os.Getenv("HOME"), ".local", "share", "icons", app.CpakId+".png")
	if err := os.MkdirAll(filepath.Dir(legacyIcon), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyIcon, []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.createExports(app); err != nil {
		t.Fatal(err)
	}

	destination := desktopEntryExportPath(app, entry)
	if !regexp.MustCompile(`^cpak-[a-f0-9]{64}-example\.desktop$`).MatchString(filepath.Base(destination)) {
		t.Fatalf("invalid desktop entry ID: %s", filepath.Base(destination))
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Exec=cpak run github.com/containerpak/example @/usr/bin/example -- --test %U") {
		t.Fatalf("desktop entry does not launch through cpak: %q", content)
	}
	if !strings.Contains(string(content), "TryExec=cpak") {
		t.Fatalf("desktop entry checks guest binary availability: %q", content)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy desktop entry directory still exists: %s", legacyDir)
	}
	if _, err := os.Stat(legacyIcon); !os.IsNotExist(err) {
		t.Fatalf("legacy icon still exists: %s", legacyIcon)
	}
}

func TestRewriteDesktopExecPreservesQuotedBinary(t *testing.T) {
	got := rewriteDesktopExec("github.com/containerpak/example", `"/opt/Example App/example" --new-window %U`)
	want := `Exec=cpak run github.com/containerpak/example "@/opt/Example App/example" -- --new-window %U`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
