/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
	launcher, err := desktopLauncherPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Exec="+desktopExecArgument(launcher)+" run --desktop-launch github.com/containerpak/example @/usr/bin/example -- --test "+desktopFileArgumentStart+" %U "+desktopFileArgumentEnd) {
		t.Fatalf("desktop entry does not launch through cpak: %q", content)
	}
	if !strings.Contains(string(content), "TryExec="+launcher) {
		t.Fatalf("desktop entry checks guest binary availability: %q", content)
	}
	alias := originalDesktopEntryExportPath(entry)
	aliasContent, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"NoDisplay=true",
		"X-cpak-Origin=github.com/containerpak/example",
		"X-cpak-ID=unsafe/base64=id",
	} {
		if !strings.Contains(string(aliasContent), value) {
			t.Fatalf("desktop alias is missing %q: %s", value, aliasContent)
		}
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("legacy desktop entry directory still exists: %s", legacyDir)
	}
	if _, err := os.Stat(legacyIcon); !os.IsNotExist(err) {
		t.Fatalf("legacy icon still exists: %s", legacyIcon)
	}
}

func TestExportDesktopAliasDoesNotReplaceUserEntry(t *testing.T) {
	c := newTestCpak(t)
	layer := "desktop-layer"
	layerDir := c.GetInStoreDir("layers", layer)
	entry := "/usr/share/applications/example.desktop"
	entryPath := filepath.Join(layerDir, strings.TrimLeft(entry, "/"))
	if err := os.MkdirAll(filepath.Dir(entryPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("[Desktop Entry]\nName=Example\nExec=/usr/bin/example\n"), 0644); err != nil {
		t.Fatal(err)
	}
	alias := originalDesktopEntryExportPath(entry)
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}
	const existing = "[Desktop Entry]\nName=User application\n"
	if err := os.WriteFile(alias, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	app := types.Application{
		CpakId:               "application-id",
		Origin:               "github.com/containerpak/example",
		ParsedDesktopEntries: []string{entry},
		ParsedLayers:         []string{layer},
	}
	if err := c.createExports(app); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(alias)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != existing {
		t.Fatalf("user desktop entry was replaced: %q", content)
	}
}

func TestRemoveDesktopAliasChecksPackageIdentity(t *testing.T) {
	newTestCpak(t)
	entry := "example.desktop"
	path := originalDesktopEntryExportPath(entry)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	content := "[Desktop Entry]\nX-cpak-Origin=github.com/containerpak/example\nX-cpak-ID=new-id\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	old := types.Application{CpakId: "old-id", Origin: "github.com/containerpak/example"}
	if err := removeDesktopAlias(old, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("new alias was removed with the old package: %v", err)
	}
	updated := types.Application{CpakId: "new-id", Origin: old.Origin}
	if err := removeDesktopAlias(updated, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("owned alias was not removed: %v", err)
	}
}

func TestRewriteDesktopExecPreservesQuotedBinary(t *testing.T) {
	got := rewriteDesktopExec("/home/user/.local/bin/cpak", "github.com/containerpak/example", `"/opt/Example App/example" --new-window %U`)
	want := `Exec=/home/user/.local/bin/cpak run --desktop-launch github.com/containerpak/example "@/opt/Example App/example" -- --new-window ` + desktopFileArgumentStart + ` %U ` + desktopFileArgumentEnd
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
