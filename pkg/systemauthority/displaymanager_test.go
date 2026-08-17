/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeDataDirectoriesKeepsWhatTheServiceAlreadySearches(t *testing.T) {
	got := mergeDataDirectories("/opt/cpak/share", "/var/lib/flatpak/exports/share:/usr/local/share:/usr/share")
	want := "/opt/cpak/share:/var/lib/flatpak/exports/share:/usr/local/share:/usr/share"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if got := mergeDataDirectories("/opt/cpak/share", ""); got != "/opt/cpak/share:"+standardDataPath {
		t.Fatalf("an empty environment did not fall back to the standard path: %s", got)
	}
	if got := mergeDataDirectories("/usr/share", "/usr/local/share:/usr/share"); got != "/usr/share:/usr/local/share" {
		t.Fatalf("the session directory was repeated: %s", got)
	}
}

func TestSessionOverrideSortsAfterAnExistingDropIn(t *testing.T) {
	name := filepath.Base(systemdOverridePath("gdm"))
	for _, existing := range []string{"10-vendor.conf", "90-distro.conf", "singularity-session.conf", "override.conf"} {
		if name <= existing {
			t.Fatalf("%s does not sort after %s, so its value would be overwritten", name, existing)
		}
	}
}

func TestDataDirectoriesFromUnitReadsOnlyTheSearchPath(t *testing.T) {
	unit := "[Service]\nEnvironment=\"LANG=C\"\nEnvironment=\"XDG_DATA_DIRS=/opt/local/share:/usr/share\"\n"
	if got := dataDirectoriesFromUnit(unit); got != "/opt/local/share:/usr/share" {
		t.Fatalf("got %s from the drop-in", got)
	}
	if got := dataDirectoriesFromUnit("[Service]\nEnvironment=\"LANG=C\"\n"); got != "" {
		t.Fatalf("an unrelated variable was read as a search path: %s", got)
	}
}

func TestManagedBlockIsRewrittenInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "greetd")
	original := "# distribution defaults\nexport XDG_DATA_DIRS=\"/usr/local/share:/usr/share\"\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	existing, current := readManagedBlock(path)
	if existing != original || current != "/usr/local/share:/usr/share" {
		t.Fatalf("the existing configuration was not read back: %q %q", existing, current)
	}
	block := environmentMarker + "\nexport " + dataDirsVariable + "=\"" + mergeDataDirectories("/opt/cpak/share", current) + "\"\n"
	if err := os.WriteFile(path, []byte(existing+block), 0644); err != nil {
		t.Fatal(err)
	}
	// A second run has to replace the block instead of stacking another one.
	again, value := readManagedBlock(path)
	if again != original {
		t.Fatalf("the managed block leaked into the preserved configuration: %q", again)
	}
	if value != "/usr/local/share:/usr/share" {
		t.Fatalf("the preserved value changed: %s", value)
	}
	if err := removeManagedBlock(path); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Fatalf("removal did not restore the original file: %q", restored)
	}
}

func TestManagedBlockRemovalDropsAFileItCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gdm")
	content := environmentMarker + "\nexport " + dataDirsVariable + "=\"/opt/cpak/share\"\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeManagedBlock(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a file holding only the managed block was kept: %v", err)
	}
}

func TestUnsupportedInitReportsTheManualStep(t *testing.T) {
	manager := environmentManager{name: "GDM", services: []string{"gdm"}}
	note, err := publishDataDirectory("runit", manager, "/opt/cpak/share")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GDM", "runit", "/opt/cpak/share", dataDirsVariable} {
		if !strings.Contains(note, want) {
			t.Fatalf("the reported step does not mention %s: %s", want, note)
		}
	}
}

func TestPublishedDirectoryIsTheDataRootNotTheSessionDirectory(t *testing.T) {
	target := layoutFor("/opt/cpak")
	if target.dataDirectory() != "/opt/cpak/share" {
		t.Fatalf("got %s, want the data root a display manager scans", target.dataDirectory())
	}
	if target.sessions != filepath.Join(target.dataDirectory(), "wayland-sessions") {
		t.Fatalf("the session directory is not below the published data root: %s", target.sessions)
	}
}
