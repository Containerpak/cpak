/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const testOrigin = "github.com/user/demo"

func newTestCpak(t testing.TB) *Cpak {
	t.Helper()

	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))

	return &Cpak{
		Options: types.CpakOptions{
			StorePath:     filepath.Join(root, "store"),
			ExportsPath:   filepath.Join(root, "exports"),
			ManifestsPath: filepath.Join(root, "manifests"),
		},
		Ctx: context.Background(),
	}
}

func testCpakId(sourceType, version string) string {
	return base64.StdEncoding.EncodeToString([]byte("demo:" + sourceType + ":" + version + ":" + testOrigin))
}

func seedApplication(t *testing.T, c *Cpak, app types.Application) {
	t.Helper()

	if err := c.storeApplication(app); err != nil {
		t.Fatalf("failed to seed application: %v", err)
	}
}

func storedApplications(t *testing.T, c *Cpak) []types.Application {
	t.Helper()

	apps, err := c.GetInstalledApps()
	if err != nil {
		t.Fatalf("failed to read the store: %v", err)
	}
	return apps
}

// updateStub provides the injected update operations, so that no network,
// registry or container runtime is needed.
type updateStub struct {
	manifest      *types.CpakManifest
	latest        string
	latestErr     error
	layers        []string
	config        string
	fetchErr      error
	pullErr       error
	imageDigest   string
	installDepErr error
	exportErr     error
	stopErr       error
	onInstallDeps func() error

	fetched  int
	pulled   int
	stopped  int
	exported int
	removed  int

	lastBranch  string
	lastRelease string
	lastOrigin  string

	exportedApps []string
	removedPairs [][2]string
}

func (s *updateStub) deps() updateDeps {
	return updateDeps{
		latestRelease: func(origin string) (string, error) {
			return s.latest, s.latestErr
		},
		fetchManifest: func(origin, branch, release, commit string) (*types.CpakManifest, error) {
			s.fetched++
			s.lastBranch = branch
			s.lastRelease = release
			if s.fetchErr != nil {
				return nil, s.fetchErr
			}
			return s.manifest, nil
		},
		installDeps: func(origin string, manifest *types.CpakManifest) ([]types.Dependency, error) {
			if s.onInstallDeps != nil {
				if err := s.onInstallDeps(); err != nil {
					return nil, err
				}
			}
			if s.installDepErr != nil {
				return nil, s.installDepErr
			}
			return nil, nil
		},
		pull: func(image, cpakImageId, origin string) ([]string, string, string, error) {
			s.pulled++
			s.lastOrigin = origin
			if s.pullErr != nil {
				return nil, "", "", s.pullErr
			}
			return s.layers, s.config, s.imageDigest, nil
		},
		buildRuntime: func(layers []string, sources []types.RuntimeSource) ([]string, error) {
			return layers, nil
		},
		buildLocale: func(layers []string, image, config string, override types.Override) ([]string, error) {
			return layers, nil
		},
		stop: func(app types.Application) error {
			s.stopped++
			return s.stopErr
		},
		createExports: func(app types.Application) error {
			s.exported++
			s.exportedApps = append(s.exportedApps, app.CpakId)
			// only the export of the new installation fails, the rollback of
			// the previous one has to succeed
			if s.exportErr != nil && s.exported == 1 {
				return s.exportErr
			}
			return nil
		},
		removeExports: func(old types.Application, updated types.Application) error {
			s.removed++
			s.removedPairs = append(s.removedPairs, [2]string{old.CpakId, updated.CpakId})
			return nil
		},
	}
}

func newTestManifest() *types.CpakManifest {
	return &types.CpakManifest{
		Name:        "demo",
		Description: "demo application",
		Image:       "ghcr.io/user/demo:latest",
		Binaries:    []string{"/usr/bin/demo"},
	}
}

func TestUpdateBranchInstallRefreshesRecord(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{"oldlayer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	stub := &updateStub{manifest: newTestManifest(), layers: []string{"newlayer"}, config: "{}", imageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111"}
	results, err := c.updateWithOptions(testOrigin, stub.deps(), UpdateOptions{
		ConfirmPermissions: func([]types.UpdateResult) bool { return true },
	})
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("expected status updated, got %q (%s)", results[0].Status, results[0].Reason)
	}
	if results[0].SourceType != "branch" || results[0].NewVersion != "main" {
		t.Fatalf("unexpected result: %+v", results[0])
	}
	if stub.lastBranch != "main" || stub.lastRelease != "" {
		t.Fatalf("expected the recorded branch to be refreshed, got branch %q release %q", stub.lastBranch, stub.lastRelease)
	}
	if stub.lastOrigin != testOrigin {
		t.Fatalf("image pull used origin %q, expected %q", stub.lastOrigin, testOrigin)
	}
	if stub.exported != 1 || stub.stopped != 1 {
		t.Fatalf("expected one export and one stop, got %d and %d", stub.exported, stub.stopped)
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 {
		t.Fatalf("expected 1 stored application, got %d", len(apps))
	}
	if apps[0].CpakId != testCpakId("branch", "main") {
		t.Fatalf("unexpected stored cpak id: %s", apps[0].CpakId)
	}
	if len(apps[0].ParsedLayers) != 1 || apps[0].ParsedLayers[0] != "newlayer" {
		t.Fatalf("expected the new layers to be stored, got %v", apps[0].ParsedLayers)
	}
	if apps[0].ImageDigest != "sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Fatalf("expected the resolved image digest, got %q", apps[0].ImageDigest)
	}
}

func TestUpdateBranchInstallUpToDate(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{"layer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	stub := &updateStub{manifest: newTestManifest(), layers: []string{"layer"}, config: "{}"}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if results[0].Status != types.UpdateStatusUpToDate {
		t.Fatalf("expected status up-to-date, got %q (%s)", results[0].Status, results[0].Reason)
	}
	if stub.exported != 1 || stub.stopped != 0 {
		t.Fatalf("expected refreshed exports without a restart, got %d exports and %d stops", stub.exported, stub.stopped)
	}
}

func TestUpdateBranchInstallRefreshesOverride(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{"layer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
		Config:         "{}",
	})

	manifest := newTestManifest()
	manifest.ManifestVersion = "2.0"
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "/etc/machine-id", Access: "read-only"}}
	stub := &updateStub{manifest: manifest, layers: []string{"layer"}, config: "{}"}
	results, err := c.updateWithOptions(testOrigin, stub.deps(), UpdateOptions{
		ConfirmPermissions: func([]types.UpdateResult) bool { return true },
	})
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("expected status updated, got %q (%s)", results[0].Status, results[0].Reason)
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 || len(apps[0].ParsedOverride.Filesystem) != 1 || apps[0].ParsedOverride.Filesystem[0] != (types.FilesystemPermission{Path: "/etc/machine-id", Access: "read-only"}) {
		t.Fatalf("expected the refreshed override, got %+v", apps)
	}
}

func TestUpdateRejectsAdditionalPermissionsBeforeMutation(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:       testCpakId("branch", "main"),
		Name:         "demo",
		Version:      "main",
		Branch:       "main",
		Origin:       testOrigin,
		ParsedLayers: []string{"oldlayer"},
		Config:       "{}",
	})

	manifest := newTestManifest()
	manifest.Override.FsExtra = []string{"/etc/machine-id"}
	stub := &updateStub{manifest: manifest, layers: []string{"newlayer"}, config: "{}"}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusPermissionDenied {
		t.Fatalf("expected permission denial, got %q", results[0].Status)
	}
	if len(results[0].PermissionAdditions) != 1 || results[0].PermissionAdditions[0] != "fsExtra" {
		t.Fatalf("unexpected permission additions: %v", results[0].PermissionAdditions)
	}
	if stub.pulled != 0 || stub.exported != 0 || stub.stopped != 0 {
		t.Fatalf("update mutated state before approval: %+v", stub)
	}
	apps := storedApplications(t, c)
	if len(apps) != 1 || apps[0].ParsedLayers[0] != "oldlayer" {
		t.Fatalf("installation changed without approval: %+v", apps)
	}
}

func TestUpdateRejectsPermissionsAddedAfterPreflight(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:       testCpakId("branch", "main"),
		Name:         "demo",
		Version:      "main",
		Branch:       "main",
		Origin:       testOrigin,
		ParsedLayers: []string{"oldlayer"},
		Config:       "{}",
	})

	approved := newTestManifest()
	approved.Override.Network = true
	changed := newTestManifest()
	changed.Override.Network = true
	changed.Override.DeviceDri = true
	stub := &updateStub{manifest: approved, layers: []string{"newlayer"}, config: "{}"}
	deps := stub.deps()
	fetches := 0
	deps.fetchManifest = func(origin, branch, release, commit string) (*types.CpakManifest, error) {
		fetches++
		if fetches == 1 {
			return approved, nil
		}
		return changed, nil
	}
	results, err := c.updateWithOptions(testOrigin, deps, UpdateOptions{
		ConfirmPermissions: func([]types.UpdateResult) bool { return true },
	})
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusPermissionDenied {
		t.Fatalf("expected permission denial, got %q", results[0].Status)
	}
	if stub.pulled != 0 {
		t.Fatal("update pulled layers after the manifest changed")
	}
}

func TestUpdateRejectsSessionPermissionsBeforeMutation(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:       testCpakId("branch", "main"),
		Name:         "demo",
		Version:      "main",
		Branch:       "main",
		Origin:       testOrigin,
		ParsedLayers: []string{"oldlayer"},
		Config:       "{}",
	})

	manifest := newTestManifest()
	manifest.Sessions = []types.Session{{
		ID:         "com.example.demo",
		Name:       "Demo",
		Kind:       "kiosk",
		Entrypoint: manifest.Binaries[0],
		Override:   types.Override{DeviceDri: true},
	}}
	stub := &updateStub{manifest: manifest, layers: []string{"newlayer"}, config: "{}"}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != types.UpdateStatusPermissionDenied {
		t.Fatalf("expected permission denial, got %q", results[0].Status)
	}
	if len(results[0].PermissionAdditions) != 1 || results[0].PermissionAdditions[0] != "session:com.example.demo" {
		t.Fatalf("unexpected session permission additions: %v", results[0].PermissionAdditions)
	}
	if stub.pulled != 0 {
		t.Fatal("update pulled layers before session permission approval")
	}
}

func TestUpdateCommitInstallIsPinned(t *testing.T) {
	cases := []struct {
		name string
		app  types.Application
	}{
		{
			name: "commit only",
			app: types.Application{
				CpakId:  testCpakId("commit", "abc123"),
				Name:    "demo",
				Version: "abc123",
				Commit:  "abc123",
				Origin:  testOrigin,
			},
		},
		{
			name: "branch pinned to a commit",
			app: types.Application{
				CpakId:  testCpakId("branch", "abc123"),
				Name:    "demo",
				Version: "abc123",
				Branch:  "main",
				Commit:  "abc123",
				Origin:  testOrigin,
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			c := newTestCpak(t)
			seedApplication(t, c, testCase.app)

			stub := &updateStub{manifest: newTestManifest(), layers: []string{"layer"}}
			results, err := c.update(testOrigin, stub.deps())
			if err != nil {
				t.Fatalf("update returned an error: %v", err)
			}

			if results[0].Status != types.UpdateStatusPinned {
				t.Fatalf("expected status pinned, got %q", results[0].Status)
			}
			if stub.fetched != 0 || stub.pulled != 0 || stub.stopped != 0 {
				t.Fatalf("expected a pinned application to be skipped entirely")
			}

			apps := storedApplications(t, c)
			if len(apps) != 1 || apps[0].CpakId != testCase.app.CpakId {
				t.Fatalf("expected the pinned record to be untouched, got %+v", apps)
			}
		})
	}
}

func TestUpdateReleaseInstallMovesToLatest(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("release", "v1.0.0"),
		Name:           "demo",
		Version:        "v1.0.0",
		Release:        "v1.0.0",
		Origin:         testOrigin,
		ParsedLayers:   []string{"oldlayer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
	})

	stub := &updateStub{manifest: newTestManifest(), latest: "v2.0.0", layers: []string{"newlayer"}, config: "{}"}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if results[0].Status != types.UpdateStatusUpdated {
		t.Fatalf("expected status updated, got %q (%s)", results[0].Status, results[0].Reason)
	}
	if results[0].OldVersion != "v1.0.0" || results[0].NewVersion != "v2.0.0" {
		t.Fatalf("unexpected versions: %+v", results[0])
	}
	if stub.lastRelease != "v2.0.0" {
		t.Fatalf("expected the manifest of the latest release, got %q", stub.lastRelease)
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 {
		t.Fatalf("expected the old release record to be replaced, got %d records", len(apps))
	}
	if apps[0].CpakId != testCpakId("release", "v2.0.0") || apps[0].Release != "v2.0.0" {
		t.Fatalf("unexpected stored application: %+v", apps[0])
	}
}

func TestUpdateReleaseInstallAlreadyLatest(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:  testCpakId("release", "v1.0.0"),
		Name:    "demo",
		Version: "v1.0.0",
		Release: "v1.0.0",
		Origin:  testOrigin,
	})

	stub := &updateStub{manifest: newTestManifest(), latest: "v1.0.0", layers: []string{"layer"}}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if results[0].Status != types.UpdateStatusUpToDate {
		t.Fatalf("expected status up-to-date, got %q", results[0].Status)
	}
	if stub.fetched != 0 || stub.pulled != 0 {
		t.Fatalf("expected no manifest fetch nor pull for an application already at the latest release")
	}
	if stub.exported != 1 {
		t.Fatalf("expected current exports to be refreshed, got %d refreshes", stub.exported)
	}
}

func TestUpdateReleaseInstallFailsWhenExportsCannotBeRefreshed(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:  testCpakId("release", "v1.0.0"),
		Name:    "demo",
		Version: "v1.0.0",
		Release: "v1.0.0",
		Origin:  testOrigin,
	})

	stub := &updateStub{latest: "v1.0.0", exportErr: errors.New("export failed")}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusFailed {
		t.Fatalf("expected status failed, got %q", results[0].Status)
	}
}

func TestUpdateReleaseInstallOnUnsupportedHost(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:       testCpakId("release", "v1.0.0"),
		Name:         "demo",
		Version:      "v1.0.0",
		Release:      "v1.0.0",
		Origin:       testOrigin,
		ParsedLayers: []string{"oldlayer"},
	})

	stub := &updateStub{
		manifest:  newTestManifest(),
		latestErr: fmt.Errorf("%w: example.com", ErrLatestReleaseUnsupported),
		layers:    []string{"newlayer"},
	}
	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if results[0].Status != types.UpdateStatusUnsupported {
		t.Fatalf("expected status unsupported, got %q", results[0].Status)
	}
	if results[0].Reason == "" {
		t.Fatalf("expected a reason explaining why the host is unsupported")
	}
	if stub.fetched != 0 || stub.stopped != 0 {
		t.Fatalf("expected the installation to be left alone")
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 || apps[0].ParsedLayers[0] != "oldlayer" {
		t.Fatalf("expected the old record to be preserved, got %+v", apps)
	}
}

func TestUpdatePreservesInstallationOnFailure(t *testing.T) {
	cases := []struct {
		name          string
		stub          *updateStub
		expectStopped int
		expectRestore bool
	}{
		{
			name: "manifest fetch failure",
			stub: &updateStub{manifest: newTestManifest(), fetchErr: errors.New("fetch failed")},
		},
		{
			name: "dependency failure",
			stub: &updateStub{manifest: newTestManifest(), installDepErr: errors.New("dependency failed")},
		},
		{
			name: "image failure",
			stub: &updateStub{manifest: newTestManifest(), pullErr: errors.New("pull failed")},
		},
		{
			name: "invalid manifest",
			stub: &updateStub{manifest: &types.CpakManifest{Name: "demo"}},
		},
		{
			name:          "export failure",
			stub:          &updateStub{manifest: newTestManifest(), layers: []string{"newlayer"}, exportErr: errors.New("export failed")},
			expectRestore: true,
		},
		{
			name:          "stop failure",
			stub:          &updateStub{manifest: newTestManifest(), layers: []string{"newlayer"}, stopErr: errors.New("stop failed")},
			expectStopped: 1,
			expectRestore: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			c := newTestCpak(t)
			seedApplication(t, c, types.Application{
				CpakId:         testCpakId("branch", "main"),
				Name:           "demo",
				Version:        "main",
				Branch:         "main",
				Origin:         testOrigin,
				ParsedLayers:   []string{"oldlayer"},
				ParsedBinaries: []string{"/usr/bin/demo"},
			})

			results, err := c.update(testOrigin, testCase.stub.deps())
			if err != nil {
				t.Fatalf("update returned an error: %v", err)
			}

			if results[0].Status != types.UpdateStatusFailed {
				t.Fatalf("expected status failed, got %q", results[0].Status)
			}
			if results[0].Err == nil {
				t.Fatalf("expected the failure to carry its error")
			}
			if testCase.stub.stopped != testCase.expectStopped {
				t.Fatalf("expected %d stops, got %d", testCase.expectStopped, testCase.stub.stopped)
			}

			apps := storedApplications(t, c)
			if len(apps) != 1 {
				t.Fatalf("expected 1 stored application, got %d", len(apps))
			}
			if apps[0].CpakId != testCpakId("branch", "main") || apps[0].ParsedLayers[0] != "oldlayer" {
				t.Fatalf("expected the working installation to be preserved, got %+v", apps[0])
			}

			if !testCase.expectRestore {
				if testCase.stub.exported != 0 {
					t.Fatalf("expected no export before the replacement, got %d", testCase.stub.exported)
				}
				return
			}

			if testCase.stub.exported != 2 {
				t.Fatalf("expected the exports of the previous installation to be restored, got %d exports", testCase.stub.exported)
			}
			if testCase.stub.exportedApps[1] != testCpakId("branch", "main") {
				t.Fatalf("expected the previous application to be exported again, got %s", testCase.stub.exportedApps[1])
			}
			if len(testCase.stub.removedPairs) != 1 {
				t.Fatalf("expected the exports of the aborted update to be removed once, got %d", len(testCase.stub.removedPairs))
			}
			if testCase.stub.removedPairs[0][1] != testCpakId("branch", "main") {
				t.Fatalf("expected the rollback to keep the exports of the previous application, got %v", testCase.stub.removedPairs[0])
			}
		})
	}
}

func TestReplaceApplicationDoesNotDuplicateRecords(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		CpakId:  testCpakId("release", "v1.0.0"),
		Name:    "demo",
		Version: "v1.0.0",
		Release: "v1.0.0",
		Origin:  testOrigin,
	}
	seedApplication(t, c, app)

	updated := app
	updated.CpakId = testCpakId("release", "v2.0.0")
	updated.Version = "v2.0.0"
	updated.Release = "v2.0.0"

	if err := c.replaceApplication(app, updated); err != nil {
		t.Fatalf("replaceApplication returned an error: %v", err)
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 || apps[0].CpakId != updated.CpakId {
		t.Fatalf("expected only the updated record, got %+v", apps)
	}
}

func TestReplaceApplicationRestoresTheRecordItRemoved(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		CpakId:  testCpakId("release", "v1.0.0"),
		Name:    "demo",
		Version: "v1.0.0",
		Release: "v1.0.0",
		Origin:  testOrigin,
	}
	seedApplication(t, c, app)

	// an application without a cpak id is rejected by the store
	err := c.replaceApplication(app, types.Application{Name: "demo", Origin: testOrigin})
	if err == nil {
		t.Fatalf("expected replaceApplication to fail on an invalid application")
	}

	apps := storedApplications(t, c)
	if len(apps) != 1 || apps[0].CpakId != app.CpakId {
		t.Fatalf("expected the previous record to be restored, got %+v", apps)
	}
}

func TestUpdateKeepsTheRecordWhenDependenciesAreInstalled(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{"oldlayer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
	})

	stub := &updateStub{manifest: newTestManifest(), pullErr: errors.New("pull failed")}
	// a dependency install adds records to the store before the update fails
	stub.onInstallDeps = func() error {
		return c.storeApplication(types.Application{
			CpakId:  "dependency",
			Name:    "dependency",
			Version: "main",
			Branch:  "main",
			Origin:  "github.com/user/dependency",
		})
	}

	results, err := c.update(testOrigin, stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}
	if results[0].Status != types.UpdateStatusFailed {
		t.Fatalf("expected status failed, got %q", results[0].Status)
	}

	apps := storedApplications(t, c)
	if len(apps) != 2 {
		t.Fatalf("expected the dependency to be kept next to the application, got %d records", len(apps))
	}
	for _, app := range apps {
		if app.Origin != testOrigin {
			continue
		}
		if app.CpakId != testCpakId("branch", "main") || app.ParsedLayers[0] != "oldlayer" {
			t.Fatalf("expected the updated application to be untouched, got %+v", app)
		}
	}
}

func TestDependencySelectorsAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name       string
		dependency types.Dependency
		branch     string
		release    string
		commit     string
	}{
		{name: "defaults to main", dependency: types.Dependency{}, branch: "main"},
		{name: "branch", dependency: types.Dependency{Branch: "devel"}, branch: "devel"},
		{name: "release", dependency: types.Dependency{Release: "v1.0.0"}, release: "v1.0.0"},
		{name: "commit", dependency: types.Dependency{Commit: "abc123"}, commit: "abc123"},
		{name: "release wins over branch", dependency: types.Dependency{Branch: "main", Release: "v1.0.0"}, release: "v1.0.0"},
		{name: "commit wins over branch", dependency: types.Dependency{Branch: "main", Commit: "abc123"}, commit: "abc123"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			branch, release, commit := dependencySelectors(testCase.dependency)
			if branch != testCase.branch || release != testCase.release || commit != testCase.commit {
				t.Fatalf("expected %q/%q/%q, got %q/%q/%q", testCase.branch, testCase.release, testCase.commit, branch, release, commit)
			}

			set := 0
			for _, selector := range []string{branch, release, commit} {
				if selector != "" {
					set++
				}
			}
			if set != 1 {
				t.Fatalf("expected exactly one selector, got %d", set)
			}
		})
	}
}

func TestFindInstalledDependency(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:  "branch-record",
		Name:    "demo",
		Version: "main",
		Branch:  "main",
		Origin:  testOrigin,
	})
	seedApplication(t, c, types.Application{
		CpakId:  "release-record",
		Name:    "demo",
		Version: "v1.0.0",
		Release: "v1.0.0",
		Origin:  testOrigin,
	})
	seedApplication(t, c, types.Application{
		CpakId:  "commit-record",
		Name:    "demo",
		Version: "abc123",
		Commit:  "abc123",
		Origin:  testOrigin,
	})

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		t.Fatalf("failed to open the store: %v", err)
	}
	defer store.Close()

	cases := []struct {
		name       string
		dependency types.Dependency
		expected   string
	}{
		{name: "branch", dependency: types.Dependency{Origin: testOrigin, Branch: "main"}, expected: "branch-record"},
		{name: "release", dependency: types.Dependency{Origin: testOrigin, Release: "v1.0.0"}, expected: "release-record"},
		{name: "commit", dependency: types.Dependency{Origin: testOrigin, Commit: "abc123"}, expected: "commit-record"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, err := findInstalledDependency(store, testCase.dependency)
			if err != nil {
				t.Fatalf("findInstalledDependency returned an error: %v", err)
			}
			if app.CpakId != testCase.expected {
				t.Fatalf("expected %s, got %s", testCase.expected, app.CpakId)
			}
		})
	}
}

func TestUpdateAllInstalledApplications(t *testing.T) {
	c := newTestCpak(t)
	seedApplication(t, c, types.Application{
		CpakId:         testCpakId("branch", "main"),
		Name:           "demo",
		Version:        "main",
		Branch:         "main",
		Origin:         testOrigin,
		ParsedLayers:   []string{"oldlayer"},
		ParsedBinaries: []string{"/usr/bin/demo"},
	})
	seedApplication(t, c, types.Application{
		CpakId:  "other",
		Name:    "other",
		Version: "abc123",
		Commit:  "abc123",
		Origin:  "github.com/user/other",
	})

	stub := &updateStub{manifest: newTestManifest(), layers: []string{"newlayer"}, config: "{}"}
	results, err := c.update("", stub.deps())
	if err != nil {
		t.Fatalf("update returned an error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	statuses := map[string]types.UpdateStatus{}
	for _, result := range results {
		statuses[result.Origin] = result.Status
	}
	if statuses[testOrigin] != types.UpdateStatusUpdated {
		t.Fatalf("expected the branch application to be updated, got %q", statuses[testOrigin])
	}
	if statuses["github.com/user/other"] != types.UpdateStatusPinned {
		t.Fatalf("expected the commit application to be pinned, got %q", statuses["github.com/user/other"])
	}
}

func TestUpdateUnknownOrigin(t *testing.T) {
	c := newTestCpak(t)
	stub := &updateStub{manifest: newTestManifest()}

	_, err := c.update("github.com/user/missing", stub.deps())
	if err == nil {
		t.Fatalf("expected an error for an origin which is not installed")
	}
}

func TestRemoveStaleExports(t *testing.T) {
	c := newTestCpak(t)

	old := types.Application{
		CpakId:         testCpakId("release", "v1.0.0"),
		Origin:         testOrigin,
		ParsedBinaries: []string{"/usr/bin/demo", "/usr/bin/gone"},
	}
	updated := types.Application{
		CpakId:         testCpakId("release", "v2.0.0"),
		Origin:         testOrigin,
		ParsedBinaries: []string{"/usr/bin/demo"},
	}

	if err := c.exportBinary(old, "/usr/bin/demo"); err != nil {
		t.Fatalf("failed to export a binary: %v", err)
	}
	if err := c.exportBinary(old, "/usr/bin/gone"); err != nil {
		t.Fatalf("failed to export a binary: %v", err)
	}

	desktopDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications", old.CpakId)
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		t.Fatalf("failed to create the desktop entries directory: %v", err)
	}

	if err := c.removeStaleExports(old, updated); err != nil {
		t.Fatalf("removeStaleExports returned an error: %v", err)
	}

	exportsDir := filepath.Join(append([]string{c.Options.ExportsPath}, "github.com", "user", "demo")...)
	if _, err := os.Stat(filepath.Join(exportsDir, "demo")); err != nil {
		t.Fatalf("expected the binary still provided to be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(exportsDir, "gone")); !os.IsNotExist(err) {
		t.Fatalf("expected the binary no longer provided to be removed")
	}
	if _, err := os.Stat(desktopDir); !os.IsNotExist(err) {
		t.Fatalf("expected the desktop entries of the replaced record to be removed")
	}
}

func TestRemoveStaleExportsForSameInstallation(t *testing.T) {
	c := newTestCpak(t)
	cpakID := testCpakId("branch", "main")
	desktopDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		t.Fatalf("failed to create the desktop entries directory: %v", err)
	}
	app := types.Application{CpakId: cpakID}
	for _, name := range []string{"demo.desktop", "gone.desktop"} {
		if err := os.WriteFile(desktopEntryExportPath(app, name), []byte("[Desktop Entry]\n"), 0644); err != nil {
			t.Fatalf("failed to create desktop entry %s: %v", name, err)
		}
	}

	old := types.Application{
		CpakId:               cpakID,
		Origin:               testOrigin,
		ParsedDesktopEntries: []string{"/usr/share/applications/demo.desktop", "/usr/share/applications/gone.desktop"},
	}
	updated := types.Application{
		CpakId:               cpakID,
		Origin:               testOrigin,
		ParsedDesktopEntries: []string{"/usr/share/applications/demo.desktop"},
	}

	if err := c.removeStaleExports(old, updated); err != nil {
		t.Fatalf("removeStaleExports returned an error: %v", err)
	}
	if _, err := os.Stat(desktopEntryExportPath(old, "demo.desktop")); err != nil {
		t.Fatalf("expected the retained desktop entry to remain: %v", err)
	}
	if _, err := os.Stat(desktopEntryExportPath(old, "gone.desktop")); !os.IsNotExist(err) {
		t.Fatalf("expected the removed desktop entry to be deleted")
	}
}

func TestCreateExportsReportsMissingDesktopEntry(t *testing.T) {
	c := newTestCpak(t)
	app := types.Application{
		CpakId:               testCpakId("branch", "main"),
		Origin:               testOrigin,
		ParsedDesktopEntries: []string{"/usr/share/applications/missing.desktop"},
		ParsedLayers:         []string{"missing-layer"},
	}

	if err := c.createExports(app); err == nil {
		t.Fatalf("expected a missing desktop entry to fail export creation")
	}
}
