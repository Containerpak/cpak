/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useAppArmorPaths(t *testing.T, restriction, profile string) {
	t.Helper()
	savedRestriction := appArmorRestrictionPath
	savedProfile := appArmorProfilePath
	savedFind := findAppArmorParser
	savedPrepare := prepareAppArmorDirectory
	savedRun := runAppArmorParser
	appArmorRestrictionPath = restriction
	appArmorProfilePath = profile
	t.Cleanup(func() {
		appArmorRestrictionPath = savedRestriction
		appArmorProfilePath = savedProfile
		findAppArmorParser = savedFind
		prepareAppArmorDirectory = savedPrepare
		runAppArmorParser = savedRun
	})
}

func TestAppArmorRuntimeRecognizesOnlyItsOwnActiveProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current")
	saved := appArmorCurrentPath
	appArmorCurrentPath = path
	t.Cleanup(func() { appArmorCurrentPath = saved })
	for value, want := range map[string]bool{
		"cpak-userns (unconfined)\n":       true,
		"unprivileged_userns (enforce)\n":  false,
		"unconfined\n":                     false,
		"cpak-userns-extra (unconfined)\n": false,
	} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if got := AppArmorRuntimeActive(); got != want {
			t.Fatalf("profile %q reported %t, want %t", value, got, want)
		}
	}
}

func TestAppArmorRestrictionRequiresExactEnabledValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restriction")
	useAppArmorPaths(t, path, filepath.Join(t.TempDir(), "cpak-userns"))
	for value, want := range map[string]bool{"0\n": false, "1\n": true, "10\n": false, "": false} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if got := AppArmorUserNamespacesRestricted(); got != want {
			t.Fatalf("restriction %q reported %t, want %t", value, got, want)
		}
	}
}

func TestAppArmorProfileNamesOnlyTheInstalledBinary(t *testing.T) {
	binary := "/opt/cpak/bin/cpak"
	profile := string(renderedAppArmorProfile(binary))
	for _, required := range []string{
		"abi <abi/4.0>,",
		"profile " + appArmorProfileName + " \"" + binary + "\" flags=(unconfined)",
		"  allow userns create,",
	} {
		if !strings.Contains(profile, required) {
			t.Fatalf("AppArmor profile does not contain %q:\n%s", required, profile)
		}
	}
	for _, forbidden := range []string{"@BINARY@", "/home/", "kernel.apparmor_restrict_unprivileged_userns = 0"} {
		if strings.Contains(profile, forbidden) {
			t.Fatalf("AppArmor profile contains %q:\n%s", forbidden, profile)
		}
	}
}

func TestAppArmorRuntimeCopyMustMatchTheUserExecutable(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	if err := os.WriteFile(first, []byte("same cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("same cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	if !SameExecutableContents(first, second) {
		t.Fatal("matching cpak binaries were reported as different")
	}
	if err := os.WriteFile(second, []byte("older cpak"), 0755); err != nil {
		t.Fatal(err)
	}
	if SameExecutableContents(first, second) {
		t.Fatal("different cpak binaries were reported as matching")
	}
}

func TestAppArmorRuntimeInstallsItsStorageServiceBesideCpak(t *testing.T) {
	directory := t.TempDir()
	savedPrepare := prepareSystemBinaryDirectory
	prepareSystemBinaryDirectory = func(path string) error { return os.MkdirAll(path, 0755) }
	t.Cleanup(func() { prepareSystemBinaryDirectory = savedPrepare })
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "runtime", "cpak-storaged")
	if err := os.WriteFile(source, []byte("storage service"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := copyExecutable(source, destination, "cpak storage service"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "storage service" {
		t.Fatalf("installed storage service contains %q", content)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("installed storage service mode is %#o", info.Mode().Perm())
	}
}

func TestAppArmorProfileIsNotInstalledWhenRestrictionIsOff(t *testing.T) {
	root := t.TempDir()
	restriction := filepath.Join(root, "restriction")
	profile := filepath.Join(root, "apparmor.d", "cpak-userns")
	if err := os.WriteFile(restriction, []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	useAppArmorPaths(t, restriction, profile)
	findAppArmorParser = func() (string, error) {
		t.Fatal("AppArmor parser was requested while the restriction was off")
		return "", nil
	}
	if err := installAppArmorProfile(layoutFor("/opt/cpak")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("profile was written while the restriction was off: %v", err)
	}
}

func TestAppArmorProfileIsLoadedBeforeItIsPublished(t *testing.T) {
	root := t.TempDir()
	restriction := filepath.Join(root, "restriction")
	profile := filepath.Join(root, "apparmor.d", "cpak-userns")
	if err := os.WriteFile(restriction, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	useAppArmorPaths(t, restriction, profile)
	findAppArmorParser = func() (string, error) { return "/test/apparmor_parser", nil }
	prepareAppArmorDirectory = func(path string) error { return os.MkdirAll(path, 0755) }
	runAppArmorParser = func(path string, arguments ...string) ([]byte, error) {
		if path != "/test/apparmor_parser" || len(arguments) != 2 || arguments[0] != "-r" {
			t.Fatalf("unexpected parser call: %s %v", path, arguments)
		}
		if arguments[1] == profile {
			t.Fatal("profile was published before the parser accepted it")
		}
		if _, err := os.Stat(arguments[1]); err != nil {
			t.Fatalf("candidate profile is unavailable to the parser: %v", err)
		}
		return nil, nil
	}
	target := layoutFor("/opt/cpak")
	if err := installAppArmorProfile(target); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(renderedAppArmorProfile(target.binary)) {
		t.Fatalf("published profile is wrong:\n%s", content)
	}
}

func TestAppArmorParserFailureIsReportedWithItsOutput(t *testing.T) {
	root := t.TempDir()
	restriction := filepath.Join(root, "restriction")
	profile := filepath.Join(root, "cpak-userns")
	if err := os.WriteFile(restriction, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	useAppArmorPaths(t, restriction, profile)
	findAppArmorParser = func() (string, error) { return "/test/apparmor_parser", nil }
	prepareAppArmorDirectory = func(path string) error { return os.MkdirAll(path, 0755) }
	runAppArmorParser = func(path string, arguments ...string) ([]byte, error) {
		return []byte("policy rejected"), errors.New("exit status 1")
	}
	err := installAppArmorProfile(layoutFor("/opt/cpak"))
	if err == nil || !strings.Contains(err.Error(), "policy rejected") {
		t.Fatalf("parser failure was reported as %v", err)
	}
}
