/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

// deploy lays an application out the way Flatpak deploys it: a current link
// to the branch, an active link to the commit and the deploy file inside it.
func deploy(t *testing.T, root, id, branch, origin, commit string) {
	t.Helper()
	directory := filepath.Join(root, "app", id, "x86_64", branch, commit)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := gvariant.Encode("(ssasta{sv})", []any{origin, commit, []any{}, uint64(4096), []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "deploy"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(commit, filepath.Join(root, "app", id, "x86_64", branch, "active")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x86_64/"+branch, filepath.Join(root, "app", id, "current")); err != nil {
		t.Fatal(err)
	}
}

func writeRepoConfig(t *testing.T, root, config string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationListsDeployedApplications(t *testing.T) {
	root := t.TempDir()
	deploy(t, root, "org.gnome.Calculator", "stable", "flathub", "00516a24")
	deploy(t, root, "org.example.Beta", "beta", "example", "1234abcd")
	if err := os.MkdirAll(filepath.Join(root, "app", "org.example.Removed", "x86_64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "app", "not an id"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoConfig(t, root, "[core]\nmode=bare-user-only\n\n[remote \"flathub\"]\nurl=https://dl.flathub.org/repo/\ngpg-verify=true\n")
	if err := os.WriteFile(filepath.Join(root, "repo", "flathub.trustedkeys.gpg"), flathubKey, 0o644); err != nil {
		t.Fatal(err)
	}

	installation := Installation{Kind: UserInstallation, Path: root}
	apps, err := installation.Apps()
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 {
		t.Fatalf("found %d applications, a leftover directory was taken for one: %+v", len(apps), apps)
	}
	if apps[0].Ref.String() != "app/org.example.Beta/x86_64/beta" || apps[0].Origin != "example" || apps[0].Commit != "1234abcd" {
		t.Fatalf("read %+v", apps[0])
	}
	if apps[1].Ref.String() != "app/org.gnome.Calculator/x86_64/stable" || apps[1].Origin != "flathub" {
		t.Fatalf("read %+v", apps[1])
	}
	location, err := installation.RemoteURL("flathub")
	if err != nil || location != "https://dl.flathub.org/repo/" {
		t.Fatalf("the remote address read as %q: %v", location, err)
	}
	key, err := installation.RemoteKey("flathub")
	if err != nil || string(key) != string(flathubKey) {
		t.Fatalf("the remote key was not read: %v", err)
	}
	if _, err := installation.RemoteURL("example"); err == nil {
		t.Fatal("a remote the installation does not configure was given an address")
	}
	if apps, err := (Installation{Kind: SystemInstallation, Path: filepath.Join(root, "absent")}).Apps(); err != nil || len(apps) != 0 {
		t.Fatalf("a missing installation answered %v, %v", apps, err)
	}
}

func TestInstallationGivesNoKeyForUnsignedRemotes(t *testing.T) {
	root := t.TempDir()
	writeRepoConfig(t, root, "[remote \"open\"]\nurl=https://example.org/repo/\ngpg-verify=false\n\n[remote \"keyless\"]\nurl=https://example.org/other/\n")
	if err := os.WriteFile(filepath.Join(root, "repo", "open.trustedkeys.gpg"), flathubKey, 0o644); err != nil {
		t.Fatal(err)
	}
	installation := Installation{Kind: UserInstallation, Path: root}
	if _, err := installation.RemoteKey("open"); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("a remote with signature checks off was given a key: %v", err)
	}
	if _, err := installation.RemoteKey("keyless"); err == nil {
		t.Fatal("a remote without a key file was given one")
	}
}

func TestInstallationRefusesADeploymentNamingAPathAsItsRemote(t *testing.T) {
	root := t.TempDir()
	deploy(t, root, "org.example.App", "stable", "../flathub", "1234abcd")
	if _, err := (Installation{Kind: UserInstallation, Path: root}).Apps(); err == nil {
		t.Fatal("a deploy file naming ../flathub as its remote was accepted")
	}
}

func TestHostInstallationsFollowTheFlatpakVariables(t *testing.T) {
	t.Setenv("FLATPAK_USER_DIR", "/srv/user-flatpak")
	t.Setenv("FLATPAK_SYSTEM_DIR", "/srv/system-flatpak")
	installations := HostInstallations()
	if len(installations) != 2 || installations[0] != (Installation{Kind: UserInstallation, Path: "/srv/user-flatpak"}) || installations[1] != (Installation{Kind: SystemInstallation, Path: "/srv/system-flatpak"}) {
		t.Fatalf("installations: %+v", installations)
	}
	t.Setenv("FLATPAK_USER_DIR", "")
	t.Setenv("XDG_DATA_HOME", "/srv/data")
	if installations := HostInstallations(); installations[0].Path != "/srv/data/flatpak" {
		t.Fatalf("the user installation is %s", installations[0].Path)
	}
}
