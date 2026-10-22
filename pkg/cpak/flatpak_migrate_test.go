/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/flatpak"
	"github.com/mirkobrombin/cpak/pkg/gvariant"
)

// hostInstallation lays out a Flatpak installation holding org.example.App
// from a remote with the given name, address and key.
func hostInstallation(t *testing.T, remote, location string, key []byte) flatpak.InstalledApp {
	t.Helper()
	root := t.TempDir()
	commit := strings.Repeat("ab", 32)
	directory := filepath.Join(root, "app", "org.example.App", "x86_64", "stable", commit)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := gvariant.Encode("(ssasta{sv})", []any{remote, commit, []any{}, uint64(0), []any{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "deploy"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(commit, filepath.Join(filepath.Dir(directory), "active")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x86_64/stable", filepath.Join(root, "app", "org.example.App", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"" + remote + "\"]\nurl=" + location + "\ngpg-verify=true\n"
	if err := os.WriteFile(filepath.Join(root, "repo", "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repo", remote+".trustedkeys.gpg"), key, 0o644); err != nil {
		t.Fatal(err)
	}
	apps, err := (flatpak.Installation{Kind: flatpak.UserInstallation, Path: root}).Apps()
	if err != nil || len(apps) != 1 {
		t.Fatalf("the host installation reads as %v: %v", apps, err)
	}
	return apps[0]
}

func TestMigrationKeepsTheKeyCpakAlreadyTrusts(t *testing.T) {
	c := flatpakTestCpak(t)
	repo := localRemote(t, c)
	// The host trusts the same address through another key; cpak does not
	// take it, it reads the repository through the key it already has.
	source := hostInstallation(t, "hostname", repo.URL(), ostreeTestOtherKey(t))
	plan, err := c.ResolveFlatpakMigration(source)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Remote.Name != "local" || plan.AddRemote {
		t.Fatalf("the migration read from %s (add %v)", plan.Remote.Name, plan.AddRemote)
	}
	if plan.App.Ref.String() != "app/org.example.App/x86_64/stable" {
		t.Fatalf("the migration asked for %s", plan.App.Ref)
	}
}

func TestMigrationAddsAnUnknownRemoteWithTheHostKey(t *testing.T) {
	served := flatpakTestCpak(t)
	repo := localRemote(t, served)
	c := flatpakTestCpak(t)
	source := hostInstallation(t, "example", repo.URL(), testFlatpakKey().Public())
	plan, err := c.ResolveFlatpakMigration(source)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Remote.Name != "example" || !plan.AddRemote {
		t.Fatalf("the remote was planned as %s (add %v)", plan.Remote.Name, plan.AddRemote)
	}

	clash := hostInstallation(t, "flathub", repo.URL(), testFlatpakKey().Public())
	if _, err := c.ResolveFlatpakMigration(clash); err == nil {
		t.Fatal("a host remote took the name of the built-in Flathub remote")
	}
}

func TestMigratedDataLandsInThePrivateHome(t *testing.T) {
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	c := flatpakTestCpak(t)
	c.SetStoragePreparationHandler(func(func() error) error { return nil })
	repo := localRemote(t, c)
	plan, err := c.ResolveFlatpakMigration(hostInstallation(t, "local", repo.URL(), testFlatpakKey().Public()))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InstallFlatpak(plan); err != nil {
		t.Fatal(err)
	}
	app, err := c.InstalledFlatpak(plan)
	if err != nil {
		t.Fatal(err)
	}

	data := filepath.Join(home, ".var", "app", "org.example.App")
	writeFile(t, filepath.Join(data, "config", "example.conf"), "flatpak")
	writeFile(t, filepath.Join(data, "config", "fresh.conf"), "flatpak")
	writeFile(t, filepath.Join(data, "data", "library.db"), "rows")
	writeFile(t, filepath.Join(data, "cache", "thumbnails"), "big")
	writeFile(t, filepath.Join(data, ".mozilla", "profiles.ini"), "profile")
	if err := os.Symlink("/etc", filepath.Join(data, "data", "system")); err != nil {
		t.Fatal(err)
	}
	privateHome, err := c.privateApplicationHome(app.CpakId)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(privateHome, ".config", "example.conf"), "cpak")

	result, err := c.MigrateFlatpakData(app)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kept != 1 {
		t.Fatalf("kept %d files", result.Kept)
	}
	for name, want := range map[string]string{
		".config/example.conf":    "cpak",
		".config/fresh.conf":      "flatpak",
		".local/share/library.db": "rows",
		".mozilla/profiles.ini":   "profile",
	} {
		content, err := os.ReadFile(filepath.Join(privateHome, name))
		if err != nil || string(content) != want {
			t.Fatalf("%s holds %q: %v", name, content, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(privateHome, ".cache")); !os.IsNotExist(err) {
		t.Fatal("the Flatpak cache was copied")
	}
	if link, err := os.Readlink(filepath.Join(privateHome, ".local", "share", "system")); err != nil || link != "/etc" {
		t.Fatalf("a link was followed instead of recreated: %q, %v", link, err)
	}
	if _, err := os.Stat(filepath.Join(data, "config", "example.conf")); err != nil {
		t.Fatal("the Flatpak data was moved instead of copied")
	}
}

func TestMigratedDataCannotLeaveThePrivateHome(t *testing.T) {
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	c := flatpakTestCpak(t)
	c.SetStoragePreparationHandler(func(func() error) error { return nil })
	repo := localRemote(t, c)
	plan, err := c.ResolveFlatpakMigration(hostInstallation(t, "local", repo.URL(), testFlatpakKey().Public()))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InstallFlatpak(plan); err != nil {
		t.Fatal(err)
	}
	app, err := c.InstalledFlatpak(plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".var", "app", "org.example.App", "config", "example.conf"), "flatpak")
	privateHome, err := c.privateApplicationHome(app.CpakId)
	if err != nil {
		t.Fatal(err)
	}
	// Something running in the sandbox left a link where the copy writes.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(privateHome, ".config")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MigrateFlatpakData(app); err == nil {
		t.Fatal("the copy went through a link out of the private home")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("the copy wrote outside the private home: %v", entries)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ostreeTestOtherKey(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "ostree", "testdata", "flathub", "flathub.gpg"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
