/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func testEnvironmentApplication(t *testing.T) (*Cpak, types.Environment) {
	t.Helper()
	cp := newTestCpak(t)
	t.Setenv("PATH", t.TempDir())
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Applications", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return cp, environment
}

func TestEnvironmentApplicationExportRoundTrip(t *testing.T) {
	cp, environment := testEnvironmentApplication(t)
	application := "/usr/share/applications/example.desktop"
	export := types.EnvironmentApplicationExport{
		Name:        "Example application",
		Description: "Example from the environment",
		Command:     `"/opt/Example App/example" --new-window %U`,
		IconPNG:     append([]byte{}, pngSignature...),
	}
	state, err := cp.ExportEnvironmentApplication(environment.ID, application, export)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exported || state.Application != application {
		t.Fatalf("export state: %+v", state)
	}

	desktopPath, err := environmentApplicationDesktopPath(environment.ID, application)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(desktopPath)
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := desktopLauncherPath()
	if err != nil {
		t.Fatal(err)
	}
	wantExec := "Exec=" + desktopExecArgument(launcher) + " environment shell --environment " + environment.ID + ` --command "@/opt/Example App/example" -- --new-window %U`
	if !strings.Contains(string(content), wantExec) {
		t.Fatalf("desktop entry does not launch through the environment: %s", content)
	}
	if desktopEntryValue(content, "X-cpak-Environment") != environment.ID || desktopEntryValue(content, "X-cpak-Environment-Application") != application {
		t.Fatalf("desktop entry ownership markers are missing: %s", content)
	}

	applications, err := cp.ListEnvironmentApplicationExports(environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(applications) != 1 || applications[0] != application {
		t.Fatalf("listed application exports: %v", applications)
	}

	state, err = cp.RemoveEnvironmentApplicationExport(environment.ID, application)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exported {
		t.Fatalf("application remained exported: %+v", state)
	}
	if _, err = os.Stat(desktopPath); !os.IsNotExist(err) {
		t.Fatalf("desktop entry still exists: %v", err)
	}
	iconPath, err := environmentApplicationIconPath(environment.ID, application)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(iconPath); !os.IsNotExist(err) {
		t.Fatalf("application icon still exists: %v", err)
	}
}

func TestEnvironmentApplicationExportDoesNotReplaceAnUnownedDesktopFile(t *testing.T) {
	cp, environment := testEnvironmentApplication(t)
	application := "/usr/share/applications/example.desktop"
	desktopPath, err := environmentApplicationDesktopPath(environment.ID, application)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(desktopPath), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("[Desktop Entry]\nName=User file\n")
	if err = os.WriteFile(desktopPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = cp.ExportEnvironmentApplication(environment.ID, application, types.EnvironmentApplicationExport{Name: "Example", Command: "/usr/bin/example"})
	if err == nil {
		t.Fatal("replaced an unowned desktop entry")
	}
	content, readErr := os.ReadFile(desktopPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != string(original) {
		t.Fatalf("unowned desktop entry changed: %q", content)
	}
}

func TestEnvironmentApplicationExportRejectsUnsafeMetadata(t *testing.T) {
	cp, environment := testEnvironmentApplication(t)
	tests := []struct {
		application string
		export      types.EnvironmentApplicationExport
	}{
		{application: "/usr/share/applications/../outside.desktop", export: types.EnvironmentApplicationExport{Name: "Example", Command: "/usr/bin/example"}},
		{application: "/usr/share/applications/example.desktop", export: types.EnvironmentApplicationExport{Name: "Example\nHidden", Command: "/usr/bin/example"}},
		{application: "/usr/share/applications/example.desktop", export: types.EnvironmentApplicationExport{Name: "Example", Command: `"/usr/bin/example`}},
		{application: "/usr/share/applications/example.desktop", export: types.EnvironmentApplicationExport{Name: "Example", Command: "/usr/bin/example", IconPNG: []byte("not a png")}},
	}
	for _, test := range tests {
		if _, err := cp.ExportEnvironmentApplication(environment.ID, test.application, test.export); err == nil {
			t.Fatalf("accepted unsafe application export: %+v", test)
		}
	}
}

func TestDeleteEnvironmentRemovesApplicationExports(t *testing.T) {
	cp, environment := testEnvironmentApplication(t)
	application := "/usr/share/applications/example.desktop"
	if _, err := cp.ExportEnvironmentApplication(environment.ID, application, types.EnvironmentApplicationExport{Name: "Example", Command: "/usr/bin/example"}); err != nil {
		t.Fatal(err)
	}
	desktopPath, err := environmentApplicationDesktopPath(environment.ID, application)
	if err != nil {
		t.Fatal(err)
	}
	if err = cp.DeleteEnvironment(environment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(desktopPath); !os.IsNotExist(err) {
		t.Fatalf("environment desktop entry still exists: %v", err)
	}
}
