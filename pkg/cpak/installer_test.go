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
	if !strings.Contains(string(content), "Exec="+desktopExecArgument(launcher)+" run --desktop-launch github.com/containerpak/example --desktop-file-span 1,0 @/usr/bin/example -- --test %U") {
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
	want := `Exec=/home/user/.local/bin/cpak run --desktop-launch github.com/containerpak/example --desktop-file-span 1,0 "@/opt/Example App/example" -- --new-window %U`
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// A shim that says only "cpak" is resolved through PATH, which anyone who can
// write the home can rearrange.
func TestExportBinaryNamesTheLauncherOutright(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{Origin: "github.com/containerpak/umu"}
	if err := c.exportBinary(app, "/usr/local/bin/umu-run"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Options.ExportsPath, "github.com", "containerpak", "umu", "umu-run")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.Contains(line, " run ") {
			continue
		}
		command := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "exec "))
		if len(command) == 0 || !filepath.IsAbs(command[0]) {
			t.Fatalf("the shim resolves its launcher through PATH: %q", line)
		}
	}
}

// TestInstallRefusesAGrantOnTheStoreItActuallyUses drives the real install
// entry point with the tree relocated, which is what CPAK_INSTALLATION_PATH and
// the per-path variables do and what cpak itself does to install a local
// package. A refusal written against the default layout under the home would
// let this grant through and hand the application every other container, every
// policy and every broker token in the store.
func TestInstallRefusesAGrantOnTheStoreItActuallyUses(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{
		{Path: filepath.Join(c.Options.StorePath, "containers"), Access: "read-write"},
	}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a grant on the store this installation uses")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}

// TestInstallRefusesASessionGrantOnCpakState covers the same rule for a session,
// which is launched with grants of its own.
func TestInstallRefusesASessionGrantOnCpakState(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:         "desk",
		Name:       "Desk",
		Kind:       "desktop",
		Entrypoint: "/usr/bin/test",
		Override: types.Override{
			Filesystem: []types.FilesystemPermission{{Path: c.Options.ExportsPath, Access: "read-only"}},
		},
	}}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a session grant on the exported launchers")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}

// TestInstallAcceptsAGrantThatMerelyContainsCpakState keeps the refusal narrow:
// the home holds the state on a default installation, and it is hidden again
// when it is mounted rather than refused here.
func TestInstallAcceptsAGrantThatMerelyContainsCpakState(t *testing.T) {
	c := newTestCpak(t)
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err != nil && strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("a grant that only contains the state was refused: %v", err)
	}
}

// TestInstallRefusesAGrantOnTheDeduplicatedStore covers the root nothing names.
// The file content of every installed package lives in the deduplication store,
// and the option that would name it is empty unless a configuration file said
// so: everything that opens that store resolves it through daBaDeeStoreOptions
// and takes the layout beside the store path when the option is blank. A
// refusal that read the option instead would leave the one place the bytes
// actually are as the one place a grant is still taken.
func TestInstallRefusesAGrantOnTheDeduplicatedStore(t *testing.T) {
	c := newTestCpak(t)
	if c.Options.DaBaDeeStoreOptions.Root != "" {
		t.Fatal("this test is about the root nothing named, and something named it")
	}
	resolved := c.daBaDeeStoreOptions().Root
	if resolved == "" {
		t.Fatal("the deduplication store resolved to nowhere")
	}
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{
		{Path: filepath.Join(resolved, "blobs"), Access: "read-write"},
	}
	err := c.InstallCpakWithOptions(testOrigin, manifest, "main", "", "", InstallOptions{})
	if err == nil {
		t.Fatal("accepted a grant on the deduplicated content of every installed package")
	}
	if !strings.Contains(err.Error(), "cpak's own state") {
		t.Fatalf("the install failed for another reason: %v", err)
	}
}
