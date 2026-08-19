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

// seedDesktopLayer writes one file into a layer of the store, at the path a
// package declared it under.
func seedDesktopLayer(t *testing.T, c *Cpak, layer, entry, content string) {
	t.Helper()

	path := filepath.Join(c.GetInStoreDir("layers", layer), strings.TrimLeft(entry, "/"))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// The alias is written under the name the package chose, into the directory
// GIO reads the user's default handlers from. A package that called its
// desktop entry mimeapps.list therefore decided what opens text/plain and
// http, on an ordinary install, without asking anything.
//
// An application installed before that was refused still carries the name in
// the store, and every update and repair re-exports from there, so the export
// leaves that one entry alone and carries on with the rest of the application.
func TestDeclaringAHandlerDatabaseDoesNotWriteOne(t *testing.T) {
	c := newTestCpak(t)
	// The name is only ever looked for beside the user's own launchers, so the
	// directories a launcher is read from are pinned to an empty one: whether
	// the machine running the test happens to ship a mimeapps.list of its own
	// decides nothing here.
	t.Setenv("XDG_DATA_DIRS", t.TempDir())

	handler := "/usr/share/applications/mimeapps.list"
	launcher := "/usr/share/applications/example.desktop"
	layer := "handler-layer"
	seedDesktopLayer(t, c, layer, handler, "[Default Applications]\ntext/plain=cpak-planted.desktop\nx-scheme-handler/http=cpak-planted.desktop\n")
	seedDesktopLayer(t, c, layer, launcher, "[Desktop Entry]\nName=Example\nExec=/usr/bin/example\n")

	app := types.Application{
		CpakId:               "handler-id",
		Origin:               "github.com/containerpak/handler",
		ParsedDesktopEntries: []string{handler, launcher},
		ParsedBinaries:       []string{"/usr/bin/example"},
		ParsedLayers:         []string{layer},
	}
	if err := c.createExports(app); err != nil {
		t.Fatalf("an application installed before the rule failed to export: %v", err)
	}

	planted := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", "mimeapps.list")
	if _, err := os.Stat(planted); !os.IsNotExist(err) {
		content, _ := os.ReadFile(planted)
		t.Fatalf("the user handler database was written: %v %q", err, content)
	}
	if _, err := os.Stat(desktopEntryExportPath(app, "mimeapps.list")); !os.IsNotExist(err) {
		t.Fatal("a handler database was exported under an application name")
	}

	// The rest of the application is still exported: refusing one name costs
	// the user that name, not the update.
	if _, err := os.Stat(originalDesktopEntryExportPath("example.desktop")); err != nil {
		t.Fatalf("the launcher beside the refused name was not exported: %v", err)
	}
	wrapper := filepath.Join(c.Options.ExportsPath, "github.com", "containerpak", "handler", "example")
	if _, err := os.Stat(wrapper); err != nil {
		t.Fatalf("the binary wrapper was not exported: %v", err)
	}
}

// A manifest is refused before any of it is installed, so the same declaration
// never reaches the export at all.
func TestValidateManifestRefusesADesktopEntryThatIsNotALauncher(t *testing.T) {
	for _, entry := range []string{
		"/usr/share/applications/mimeapps.list",
		"/usr/share/applications/defaults.list",
		"/usr/share/applications/mimeinfo.cache",
		"/usr/share/applications/.bashrc",
		`/usr/share/applications/..\..\config\autostart\evil.desktop`,
		"/usr/share/applications/.desktop",
	} {
		manifest := validManifestForTest()
		manifest.DesktopEntries = []string{entry}
		if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
			t.Fatalf("desktop entry %q was accepted", entry)
		}
	}

	manifest := validManifestForTest()
	manifest.DesktopEntries = []string{"/usr/share/applications/org.telegram.desktop"}
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatalf("a launcher was refused: %v", err)
	}
}

// Nothing can find the alias again once it is written unmarked: the guard that
// keeps a second package off it and the removal both read the marker back out
// of the file. A serialisation that took no marker is one cpak owns and cannot
// name, so it is not written, and not writing it is not a reason to fail the
// installation either.
func TestAnAliasThatCouldNotBeMarkedIsNotWritten(t *testing.T) {
	newTestCpak(t)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	alias := originalDesktopEntryExportPath("example.desktop")
	if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
		t.Fatal(err)
	}

	app := types.Application{CpakId: "unmarked-id", Origin: "github.com/containerpak/example"}
	if err := exportDesktopAlias(app, "example.desktop", "[Default Applications]\ntext/plain=cpak-planted.desktop\n"); err != nil {
		t.Fatalf("an unmarkable alias failed the export: %v", err)
	}

	if _, err := os.Stat(alias); !os.IsNotExist(err) {
		content, _ := os.ReadFile(alias)
		t.Fatalf("the unmarkable desktop entry was written: %v %q", err, content)
	}
}

// A .desktop file written on Windows, or one with a stray space after its
// group header, is an ordinary launcher. The marker has to land in it, because
// an alias without one is an alias the uninstall walks past.
func TestAnAliasIsMarkedThroughAnAwkwardGroupHeader(t *testing.T) {
	for name, content := range map[string]string{
		"carriage returns":  "[Desktop Entry]\r\nName=Example\r\nExec=/usr/bin/example\r\n",
		"a trailing space":  "[Desktop Entry] \nName=Example\nExec=/usr/bin/example\n",
		"a byte order mark": "\ufeff[Desktop Entry]\nName=Example\nExec=/usr/bin/example\n",
		// The group after this one never closes its bracket. The reader ends
		// the Desktop Entry group there and the writer used not to, which put
		// the marker in a group nothing reads it back out of.
		"a malformed later header": "[Desktop Entry]\nName=Example\nExec=/usr/bin/example\n[Desktop Action new\nName=New\n",
	} {
		t.Run(name, func(t *testing.T) {
			newTestCpak(t)
			t.Setenv("XDG_DATA_DIRS", t.TempDir())
			alias := originalDesktopEntryExportPath("example.desktop")
			if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
				t.Fatal(err)
			}

			app := types.Application{CpakId: "awkward-id", Origin: "github.com/containerpak/example"}
			if err := exportDesktopAlias(app, "example.desktop", content); err != nil {
				t.Fatal(err)
			}
			written, err := os.ReadFile(alias)
			if err != nil {
				t.Fatalf("the launcher was not exported: %v", err)
			}
			if desktopEntryValue(written, "X-cpak-ID") != app.CpakId {
				t.Fatalf("the alias carries no marker: %q", written)
			}

			// The marker is only worth anything if the removal finds it.
			if err := removeDesktopAlias(app, "example.desktop"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(alias); !os.IsNotExist(err) {
				t.Fatal("the alias survived the removal of the application that wrote it")
			}
		})
	}
}
