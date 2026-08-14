/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareHostApplicationCatalogSanitizesDesktopEntries(t *testing.T) {
	data := filepath.Join(t.TempDir(), "share")
	applications := filepath.Join(data, "applications")
	icons := filepath.Join(data, "icons", "hicolor", "scalable", "apps")
	if err := os.MkdirAll(applications, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(icons, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(icons, "demo.svg"), []byte("<svg/>"), 0644); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(applications, "demo.desktop")
	desktop := "[Desktop Entry]\nName=Demo\nExec=/bin/true %F\nTryExec=/bin/true\nIcon=demo\nDBusActivatable=true\nActions=New;\n\n[Desktop Action New]\nExec=/bin/true --new\n"
	if err := os.WriteFile(source, []byte(desktop), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", data)

	root, mapPath, err := prepareHostApplicationCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(filepath.Join(root, "share", "applications", "demo.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(generated)
	for _, forbidden := range []string{"/bin/true", "TryExec=", "Actions=", "DBusActivatable=true", "[Desktop Action"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("generated desktop entry contains %q: %s", forbidden, content)
		}
	}
	if !strings.Contains(content, "Exec=/usr/local/bin/cpak-launch-app ") || !strings.Contains(content, "Icon="+hostApplicationsTarget) {
		t.Fatalf("generated desktop entry is incomplete: %s", content)
	}

	catalog := map[string]string{}
	encoded, err := os.ReadFile(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &catalog); err != nil {
		t.Fatal(err)
	}
	launcherPath := catalog[hostApplicationToken(source)]
	if filepath.Dir(launcherPath) != hostApplicationLaunchersPath(filepath.Dir(mapPath)) {
		t.Fatalf("catalog maps the generated token outside the private launchers: %s", launcherPath)
	}
	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	launcherContent := string(launcher)
	if !strings.Contains(launcherContent, "Exec=/bin/true %F") || !strings.Contains(launcherContent, "DBusActivatable=false") {
		t.Fatalf("private launcher is incomplete: %s", launcherContent)
	}
	for _, forbidden := range []string{"Actions=", "DBusActivatable=true", "[Desktop Action"} {
		if strings.Contains(launcherContent, forbidden) {
			t.Fatalf("private launcher contains %q: %s", forbidden, launcherContent)
		}
	}
}

func TestPrepareHostApplicationCatalogSkipsMissingExecutables(t *testing.T) {
	data := filepath.Join(t.TempDir(), "share")
	applications := filepath.Join(data, "applications")
	if err := os.MkdirAll(applications, 0755); err != nil {
		t.Fatal(err)
	}
	desktop := "[Desktop Entry]\nName=Missing\nExec=/missing/cpak-test-application\nType=Application\n"
	if err := os.WriteFile(filepath.Join(applications, "missing.desktop"), []byte(desktop), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_DATA_DIRS", data)

	root, _, err := prepareHostApplicationCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "share", "applications", "missing.desktop")); !os.IsNotExist(err) {
		t.Fatalf("missing executable was exported: %v", err)
	}
}

func TestDesktopEntryExecutableSupportsQuotedPaths(t *testing.T) {
	got, ok := desktopEntryExecutable(`"/opt/Application Folder/demo" --open %U`)
	if !ok || got != "/opt/Application Folder/demo" {
		t.Fatalf("desktop executable: got %q, ok %t", got, ok)
	}
}
