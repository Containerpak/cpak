/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree/ostreetest"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func checkStatus(t *testing.T, c *Cpak, origin string) types.UpdateStatus {
	t.Helper()
	results, err := c.CheckUpdates(origin)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("the check answered %d results", len(results))
	}
	return results[0].Status
}

func TestCheckReportsANewFlatpakBuildWithoutApplyingIt(t *testing.T) {
	t.Setenv("LANG", "it_IT.UTF-8")
	t.Setenv("LANGUAGE", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	c := flatpakTestCpak(t)
	c.SetStoragePreparationHandler(func(func() error) error { return nil })
	repo := localRemote(t, c)
	plan, err := c.ResolveFlatpak(FlatpakRequest{ID: "org.example.App", Remote: "local", Arch: "x86_64"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InstallFlatpak(plan); err != nil {
		t.Fatal(err)
	}
	if status := checkStatus(t, c, plan.Origin); status != types.UpdateStatusUpToDate {
		t.Fatalf("a fresh installation was reported %s", status)
	}
	if status := checkStatus(t, c, "flatpak/local/org.example.platform"); status != types.UpdateStatusUpToDate {
		t.Fatalf("the runtime was reported %s on its own", status)
	}

	repo.Commit("runtime/org.example.Platform.Locale/x86_64/1", map[string]ostreetest.File{
		"files/it/share/it/LC_MESSAGES/x.mo": {Content: []byte("it, again"), Mode: 0o644},
	}, nil)
	if status := checkStatus(t, c, plan.Origin); status != types.UpdateStatusAvailable {
		t.Fatalf("a new build of a runtime extension was reported %s", status)
	}

	metadata := plan.App.Commit.String("xa.metadata")
	repo.Commit("app/org.example.App/x86_64/stable", map[string]ostreetest.File{
		"files/bin/example": {Content: []byte("#!/bin/sh\necho 2\n"), Mode: 0o755},
	}, map[string]gvariant.Variant{"xa.metadata": {Signature: "s", Value: metadata}})
	if status := checkStatus(t, c, plan.Origin); status != types.UpdateStatusAvailable {
		t.Fatalf("a new build of the application was reported %s", status)
	}
	app, err := c.InstalledFlatpak(plan)
	if err != nil {
		t.Fatal(err)
	}
	if app.Flatpak.Commit != plan.App.Commit.Checksum.String() {
		t.Fatal("the check moved the installation")
	}
}

func TestCheckNeverPullsAGitPackage(t *testing.T) {
	cases := []struct {
		name    string
		app     types.Application
		stub    *updateStub
		want    types.UpdateStatus
		version string
	}{
		{
			name: "same tag",
			app:  types.Application{Branch: "main", Version: "main", Image: "ghcr.io/user/demo:latest"},
			stub: &updateStub{manifest: newTestManifest()},
			want: types.UpdateStatusUnsupported,
		},
		{
			name: "another image",
			app:  types.Application{Branch: "main", Version: "main", Image: "ghcr.io/user/demo:old"},
			stub: &updateStub{manifest: newTestManifest()},
			want: types.UpdateStatusAvailable,
		},
		{
			name: "same digest",
			app:  types.Application{Branch: "main", Version: "main", Image: "ghcr.io/user/demo@sha256:" + strings.Repeat("a", 64)},
			stub: &updateStub{manifest: &types.CpakManifest{Name: "demo", Description: "demo application", Image: "ghcr.io/user/demo@sha256:" + strings.Repeat("a", 64), Binaries: []string{"/usr/bin/demo"}}},
			want: types.UpdateStatusUpToDate,
		},
		{
			name:    "new release",
			app:     types.Application{Release: "v1", Version: "v1"},
			stub:    &updateStub{manifest: newTestManifest(), latest: "v2"},
			want:    types.UpdateStatusAvailable,
			version: "v2",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			c := newTestCpak(t)
			app := testCase.app
			app.CpakId = testCpakId("branch", "main")
			app.Name = "demo"
			app.Origin = testOrigin
			app.ParsedBinaries = []string{"/usr/bin/demo"}
			seedApplication(t, c, app)
			results, err := c.checkUpdates(testOrigin, testCase.stub.deps())
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Status != testCase.want {
				t.Fatalf("the check answered %s (%s)", results[0].Status, results[0].Reason)
			}
			if testCase.version != "" && results[0].NewVersion != testCase.version {
				t.Fatalf("the new version is %s", results[0].NewVersion)
			}
			if testCase.stub.pulled != 0 || testCase.stub.exported != 0 || testCase.stub.stopped != 0 {
				t.Fatal("the check pulled, exported or stopped something")
			}
		})
	}
}
