/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestV2SchemaExcludesLegacyFilesystemFields(t *testing.T) {
	schema := ManifestV2Schema()
	version, ok := schema.Properties.Get("manifest_version")
	if !ok || version.Const != "2.0" {
		t.Fatalf("manifest version is not pinned to 2.0: %+v", version)
	}
	override, ok := schema.Definitions["Override"]
	if !ok {
		t.Fatal("override definition is missing")
	}
	for _, field := range []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"} {
		if _, exists := override.Properties.Get(field); exists {
			t.Fatalf("legacy field %s is present in v2 schema", field)
		}
	}
}

func TestManifestSchemaValidatesFormFactors(t *testing.T) {
	manifest := validManifestForTest()
	manifest.FormFactors = []string{"desktop", "phone", "tablet", "tv", "watch"}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("supported form factors were refused: %v", err)
	}

	manifest.FormFactors = []string{"desktop", "desktop"}
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("duplicate form factors were accepted")
	}

	manifest.FormFactors = []string{"car"}
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("an unsupported form factor was accepted")
	}
}

// The committed schema is what an editor reads and what a publisher writes
// against, and it is generated from the same call the runtime validator uses.
// It only tells the truth for as long as somebody remembers to regenerate it.
func TestCommittedSchemaMatchesTheGenerator(t *testing.T) {
	generated, err := json.MarshalIndent(ManifestV2Schema(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	generated = append(generated, '\n')
	committed, err := os.ReadFile("../../schema/manifest-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(committed) != string(generated) {
		t.Fatal("schema/manifest-v2.json is stale: run cpak gen-schema --output schema/manifest-v2.json")
	}
}

// The shape of a binary path is narrow on purpose. The wrapper the installer
// exports quotes the path it is given, so what is left to say is that the path
// is absolute and carries nothing a terminal acts on. A pattern drawn tighter
// than that is not a control, it is an outage: the first list below is what
// the packages in a real store declare, and the second is what other real
// packages ship, each of which a character class of the obvious kind refuses.
func TestManifestSchemaHoldsBinariesToAnAbsolutePathAndNothingMore(t *testing.T) {
	declaredToday := []string{
		"/usr/bin/vlc",
		"/usr/bin/c++",
		"/usr/bin/vokoscreenNG",
		"/usr/bin/x86_64-w64-mingw32-g++",
		"/usr/bin/lua5.4",
		"/usr/sbin/php-fpm8.5",
		"/usr/games/dolphin-emu",
		"/usr/local/bin/umu-run",
		"/usr/share/code/bin/code",
		"/opt/Heroic/heroic",
		"/opt/singularity/bin/singularity-cpak-session",
	}
	shippedElsewhere := []string{
		"/opt/Sublime Text/sublime_text",
		"/usr/bin/caf\u00e9",
		"/usr/lib/foo@bar/run",
		"/opt/app~1/bin/app",
		"/opt/Foo (beta)/bin/foo",
		"/usr/bin/foo,bar",
	}
	for _, binary := range append(declaredToday, shippedElsewhere...) {
		manifest := validManifestForTest()
		manifest.Binaries = []string{binary}
		if err := ValidateManifest(manifest); err != nil {
			t.Errorf("a legitimate binary path was refused: %s: %v", binary, err)
		}
	}

	// The desktop entries those same packages declare go on validating: the
	// field that gained a shape is binaries, and nothing else moved.
	entries := validManifestForTest()
	entries.DesktopEntries = []string{
		"/usr/share/applications/org.gnome.gitlab.YaLTeR.VideoTrimmer.desktop",
		"/usr/share/applications/io.github.seadve.Kooha.desktop",
		"/usr/share/applications/com.github.jeromerobert.pdfarranger.desktop",
		"/usr/share/applications/org.remmina.Remmina-file.desktop",
		"/usr/share/spotify/spotify.desktop",
		"/usr/share/applications/vokoscreenNG.desktop",
	}
	if err := ValidateManifest(entries); err != nil {
		t.Errorf("the desktop entries of an installed package were refused: %v", err)
	}

	refused := map[string]string{
		"a relative path":      "usr/bin/demo",
		"a newline":            "/usr/bin/demo\nrm -rf ~",
		"an escape sequence":   "/usr/bin/demo\x1b[2K",
		"the C1 form of it":    "/usr/bin/demo\u009b2K",
		"the root directory":   "/",
		"an empty declaration": "",
	}
	for name, binary := range refused {
		manifest := validManifestForTest()
		manifest.Binaries = []string{binary}
		if err := ValidateManifest(manifest); err == nil {
			t.Errorf("%s was accepted as a binary path: %q", name, binary)
		}
	}
}
