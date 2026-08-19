/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// The shapes are only a control if the schema that runs carries them, and the
// reflector silently carried none of them. This is the test that would have
// said so.
func TestManifestSchemaCarriesEveryItemPattern(t *testing.T) {
	schema := ManifestSchema()
	for _, item := range manifestItemPatterns {
		property := arrayItemSchema(schema, item.definition, item.property)
		if property == nil {
			t.Fatalf("%s has no array property %q", item.holder(), item.property)
		}
		if property.Pattern != item.pattern {
			t.Fatalf("%s items of %q carry pattern %q, expected %q", item.holder(), item.property, property.Pattern, item.pattern)
		}
	}
}

// A shape that is not written where the reflector reads it is a shape nothing
// enforces, and the tag says nothing about that at compile time. The four
// fields that declared one all spelled it the way the reflector drops.
func TestNoTypeDeclaresAnInertItemsPattern(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(source), "items.pattern=") {
			relative, _ := filepath.Rel(root, path)
			t.Errorf("%s declares an items.pattern tag, which the reflector does not emit", relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every value below is one an installed manifest on a real machine declares,
// plus the awkward but legitimate paths a stricter pattern would have refused.
// A shape drawn tighter than the values it has to accept is not a control, it
// is an outage.
func TestManifestSchemaAcceptsDeclarationsThatAreInstalledToday(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Binaries = []string{
		"/usr/bin/telegram-desktop",
		"/usr/local/bin/umu-run",
		"/opt/Heroic/heroic",
		"/usr/share/code/bin/code",
		"/usr/sbin/php-fpm8.5",
		"/usr/bin/i686-w64-mingw32-g++",
		"/opt/Sublime Text/sublime_text",
		"/usr/bin/caf\u00e9",
		"/opt/Foo (beta)/bin/foo",
		"/usr/lib/foo@bar/run",
	}
	manifest.DesktopEntries = []string{
		"/usr/share/applications/org.telegram.desktop",
		"/usr/share/applications/com.github.johnfactotum.Foliate.desktop",
		"/usr/share/spotify/spotify.desktop",
		"/usr/share/applications/org.remmina.Remmina-file.desktop",
	}
	manifest.Override.Env = []string{
		"GST_PLUGIN_SYSTEM_PATH=/app/lib/gstreamer-1.0:/usr/lib/x86_64-linux-gnu/gstreamer-1.0",
		"LANG=C.UTF-8",
		"OPENSSL_CONF=/dev/null",
		"WLR_LIBINPUT_NO_DEVICES=1",
		// Clearing a variable is a thing a package legitimately asks for.
		"LD_PRELOAD=",
	}
	manifest.Override.AllowedHostCommands = []string{"xdg-open"}

	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("a manifest that is installed today was refused: %v", err)
	}
}

func TestManifestSchemaRefusesItemsWithoutTheirShape(t *testing.T) {
	for name, apply := range map[string]func(*types.CpakManifest){
		"a relative binary": func(m *types.CpakManifest) {
			m.Binaries = []string{"usr/bin/example"}
		},
		"a desktop entry that is not a launcher": func(m *types.CpakManifest) {
			m.DesktopEntries = []string{"/usr/share/applications/mimeapps.list"}
		},
		"an environment entry that names nothing": func(m *types.CpakManifest) {
			m.Override.Env = []string{"LD_PRELOAD"}
		},
		"an environment name that is not one": func(m *types.CpakManifest) {
			m.Override.Env = []string{"a b=c"}
		},
		"a host command that is a path": func(m *types.CpakManifest) {
			m.Override.AllowedHostCommands = []string{"../../bin/sh"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := validManifestForTest()
			apply(manifest)
			if err := ValidateManifest(manifest); err == nil {
				t.Fatal("the schema accepted it")
			}
		})
	}
}
