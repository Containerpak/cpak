/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func environmentTestApplication() types.Application {
	return types.Application{
		CpakId:         "distribution-package",
		Name:           "Test distribution",
		Origin:         testOrigin,
		Version:        "1",
		Branch:         "main",
		Config:         `{}`,
		ParsedBinaries: []string{"/bin/sh"},
		ParsedOverride: types.Override{
			Network: true,
			Filesystem: []types.FilesystemPermission{
				{Path: "home", Access: "read-only"},
			},
		},
	}
}

func TestEnvironmentRoundTripUsesInstalledPackagePolicy(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)

	environment, err := cp.CreateEnvironment("Fedora Work", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if environment.ApplicationCpakId != app.CpakId || !environment.Policy.Network {
		t.Fatalf("environment did not retain the installed package policy: %+v", environment)
	}

	loaded, err := cp.GetEnvironment("fedora work")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != environment.ID || loaded.Name != environment.Name {
		t.Fatalf("loaded environment: got %+v, want %+v", loaded, environment)
	}

	listed, err := cp.ListEnvironments()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != environment.ID {
		t.Fatalf("listed environments: %+v", listed)
	}
}

func TestEnvironmentUsesItsOwnRuntimePolicyVersion(t *testing.T) {
	if containerRuntimeVersion(environmentInstance("id")) == containerRuntimeVersion("") {
		t.Fatal("environment containers can reuse a single-ID runtime")
	}
}

func TestEnvironmentNamesAreUniqueAndBounded(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	if _, err := cp.CreateEnvironment("Ubuntu", app.Origin, app.Version, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cp.CreateEnvironment("ubuntu", app.Origin, app.Version, "", "", ""); err == nil {
		t.Fatal("created two environments with the same name")
	}
	if _, err := cp.CreateEnvironment(" bad ", app.Origin, app.Version, "", "", ""); err == nil {
		t.Fatal("created an environment with surrounding whitespace")
	}
	if _, err := cp.CreateEnvironment(strings.Repeat("x", 81), app.Origin, app.Version, "", "", ""); err == nil {
		t.Fatal("created an environment with a name longer than 80 characters")
	}
}

func TestEnvironmentMetadataSizeIsBounded(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Bounded", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := cp.environmentPath(environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "environment.json")
	if err = os.WriteFile(path, bytes.Repeat([]byte(" "), environmentSizeLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = cp.GetEnvironment(environment.ID); err == nil {
		t.Fatal("loaded environment metadata larger than the configured limit")
	}
}

func TestEnvironmentPolicyCanOnlyNarrowInstalledPolicy(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Debian", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	narrowed := environment.Policy
	narrowed.Network = false
	narrowed.Filesystem = nil
	updated, err := cp.SetEnvironmentPolicy(environment.ID, narrowed)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Policy.Network || len(updated.Policy.Filesystem) != 0 {
		t.Fatalf("environment policy was not narrowed: %+v", updated.Policy)
	}

	widened := narrowed
	widened.DeviceAll = true
	if _, err = cp.SetEnvironmentPolicy(environment.ID, widened); err == nil {
		t.Fatal("environment policy granted deviceAll beyond the installed package")
	}
}

func TestEnvironmentPermissionCeilingUsesInstalledPackagePolicy(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Permissions", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	narrowed := environment.Policy
	narrowed.Network = false
	narrowed.Filesystem = nil
	if _, err = cp.SetEnvironmentPolicy(environment.ID, narrowed); err != nil {
		t.Fatal(err)
	}
	ceiling, err := cp.EnvironmentPermissionCeiling(environment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ceiling.Network || len(ceiling.Filesystem) != 1 || ceiling.Filesystem[0].Path != "home" {
		t.Fatalf("permission ceiling did not retain the installed policy: %+v", ceiling)
	}
}

func TestEnvironmentPolicyCanonicalizesEquivalentOrdering(t *testing.T) {
	policy := types.Override{
		Filesystem: []types.FilesystemPermission{
			{Path: "/var/lib/work", Access: "read-write"},
			{Path: "/etc/work.conf", Access: "read-only"},
		},
		HostActions: []types.HostActionGrant{{
			Provider: types.HostActionProviderContainers,
			Capabilities: []string{
				types.HostActionContainersRead,
				types.HostActionContainersExecOwned,
			},
		}},
		SessionBus: types.DBusPolicy{Own: []string{"org.example.Second", "org.example.First"}},
	}
	if err := validateEnvironmentPolicy(&policy); err != nil {
		t.Fatal(err)
	}
	if policy.Filesystem[0].Path != "/etc/work.conf" {
		t.Fatalf("filesystem policy was not canonicalized: %+v", policy.Filesystem)
	}
	if policy.HostActions[0].Capabilities[0] != types.HostActionContainersExecOwned {
		t.Fatalf("host action policy was not canonicalized: %+v", policy.HostActions)
	}
	if policy.SessionBus.Own[0] != "org.example.First" {
		t.Fatalf("session bus policy was not canonicalized: %+v", policy.SessionBus)
	}
}

func TestEnvironmentFollowsAnUpdatedInstalledPackage(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Rolling", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	statePath, err := cp.GetInStoreDirMkdir("states", "environment-before-update")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cp.GetInStoreDirMkdir("containers", "environment-before-update", "rootfs"); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.NewContainer(types.Container{
		CpakId:            "environment-before-update",
		ApplicationCpakId: environmentContainerScope(environment.ID),
		StatePath:         statePath,
		CreateTimestamp:   time.Now(),
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err = store.RemoveApplicationByCpakId(app.CpakId); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	updated := app
	updated.CpakId = "updated-distribution-package"
	updated.Version = "2"
	if err = store.NewApplication(updated); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	environment, err = cp.SetEnvironmentPolicy(environment.ID, environment.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if environment.ApplicationCpakId != updated.CpakId || environment.Version != updated.Version {
		t.Fatalf("environment still points at the removed package: %+v", environment)
	}
	if _, err = os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("environment container from the previous package version still exists: %v", err)
	}
}

func TestEnvironmentWritableLayerSurvivesContainerCleanup(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("openSUSE", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	persistent, err := cp.environmentPersistentState(environment)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(persistent.upperDir, "installed-package")
	if err = os.WriteFile(marker, []byte("present"), 0600); err != nil {
		t.Fatal(err)
	}

	statePath, err := cp.GetInStoreDirMkdir("states", "temporary-container")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cp.GetInStoreDirMkdir("containers", "temporary-container", "rootfs"); err != nil {
		t.Fatal(err)
	}
	container := types.Container{
		CpakId:            "temporary-container",
		ApplicationCpakId: environmentContainerScope(environment.ID),
		StatePath:         statePath,
		WritableLayerPath: persistent.upperDir,
		WritableWorkPath:  persistent.workDir,
		CreateTimestamp:   time.Now(),
	}
	store, err := NewStore(cp.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.NewContainer(container); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cp.CleanupContainer(container); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatalf("persistent writable layer was removed: %v", err)
	}
	if _, err = os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("ephemeral container state still exists: %v", err)
	}
}

func TestDeleteEnvironmentRemovesPersistentRootAndPrivateHome(t *testing.T) {
	cp := newTestCpak(t)
	app := environmentTestApplication()
	seedApplication(t, cp, app)
	environment, err := cp.CreateEnvironment("Arch", app.Origin, app.Version, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	persistent, err := cp.environmentPersistentState(environment)
	if err != nil {
		t.Fatal(err)
	}
	home, err := cp.privateApplicationHome(environmentDataID(environment.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "history"), []byte("command"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = cp.DeleteEnvironment(environment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Dir(filepath.Dir(persistent.upperDir))); !os.IsNotExist(err) {
		t.Fatalf("environment root still exists: %v", err)
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("environment private home still exists: %v", err)
	}
}
