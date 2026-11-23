/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/appservice"
	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree/ostreetest"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const replacedTopLayer = "7777777777777777777777777777777777777777777777777777777777777777"

func useListeningService(t *testing.T) {
	t.Helper()
	directory, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	service := filepath.Join(directory, serviceSocketName)
	for _, path := range []string{service, filepath.Join(directory, systemBrokerSocketName), appservice.ManagerSocketPath(service)} {
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			for {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				_ = connection.Close()
			}
		}()
	}
	t.Setenv("CPAK_SERVICE_SOCKET", service)
}

func TestStoppingAnApplicationStopsEveryInstance(t *testing.T) {
	c := newTestCpak(t)
	app := verifiedApplication()
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{
		"plain":   app.CpakId,
		"named":   ApplicationScope(app.CpakId, "work"),
		"extra":   ApplicationScope(app.CpakId, extraDataInstance+"-1"),
		"foreign": ApplicationScope(app.CpakId+"2", "work"),
	}
	for id, scope := range scopes {
		if err := store.NewContainer(types.Container{CpakId: id, ApplicationCpakId: scope, StatePath: filepath.Join(t.TempDir(), id)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.stopApplicationContainers(app); err != nil {
		t.Fatal(err)
	}
	store, err = NewStore(c.Options.StorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	left, err := store.GetContainers()
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].CpakId != "foreign" {
		t.Fatalf("containers left: %+v", left)
	}
}

func TestExtraDataWaitsForTheBuildThatIsRunning(t *testing.T) {
	c := &Cpak{Ctx: context.Background(), Options: Options{StorePath: t.TempDir()}}
	source := &types.FlatpakSource{
		Ref:       "app/com.example.Stub/x86_64/stable",
		Commit:    "abc",
		ExtraData: []types.FlatpakExtraData{{Name: "stub.deb", URI: "http://127.0.0.1:1/stub.deb", Checksum: "00", Size: 1}},
	}
	digest := flatpakExtraLayerDigest(source)
	unlock, err := c.lockContainerScope("extra-data:" + digest)
	if err != nil {
		t.Fatal(err)
	}
	app := types.Application{ParsedLayers: []string{"base"}, Flatpak: source}
	done := make(chan error, 1)
	go func() { done <- c.applyFlatpakExtraData(&app) }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("extra data was built beside a running build: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	temporary, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{Message: "extra data"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(temporary)
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writer.AddTreeProgress(tree, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := publishFVSLayer(temporary, c.fvsLayerPath(digest)); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("the finished layer was not reused: %v", err)
	}
}

func TestApplyExtraRunsAgainstTheBuildBeingInstalled(t *testing.T) {
	c := newTestCpak(t)
	useEnforcement(t, systemauthority.EnforcementRefuse)
	useEnrolmentAuthority(t)
	useListeningService(t)
	app := enrolledApplication(t, c)
	app.Config = "stops the launch after the gate"
	if enrolment := c.EnrolApplication(app); enrolment.Outcome != EnrolmentRecorded {
		t.Fatal(enrolment.Reason)
	}
	bindLayer(t, c, replacedTopLayer, "state-next")
	updated := app
	updated.ParsedLayers = []string{verifiedBaseLayer, replacedTopLayer}
	c.SetStoragePreparationHandler(func(func() error) error { return nil })
	runErr := c.runApplyExtra(updated, t.TempDir())
	if errors.Is(runErr, errLaunchUnrecognised) || !c.launchRecognised(updated) {
		t.Fatalf("apply_extra met the anchor of the build it replaces: %v", runErr)
	}
}

func TestFailedUpdateRestoresTheInstalledAnchor(t *testing.T) {
	c := newTestCpak(t)
	useEnrolmentAuthority(t)
	app := enrolledApplication(t, c)
	seedApplication(t, c, app)
	bindLayer(t, c, replacedTopLayer, "state-next")
	next := app
	next.ParsedLayers = []string{verifiedBaseLayer, replacedTopLayer}
	if enrolment := c.EnrolApplication(next); enrolment.Outcome != EnrolmentRecorded {
		t.Fatal(enrolment.Reason)
	}
	c.restoreEnrolment(app)
	if !c.launchRecognised(app) {
		t.Fatal("the installed application was left without its anchor")
	}
}

func TestRollbackMovesTheAnchorWithTheInstallation(t *testing.T) {
	for _, intact := range []bool{true, false} {
		c := newTestCpak(t)
		useEnrolmentAuthority(t)
		previous := enrolledApplication(t, c)
		bindLayer(t, c, replacedTopLayer, "state-next")
		current := previous
		current.ParsedBinaries = nil
		current.ParsedLayers = []string{verifiedBaseLayer, replacedTopLayer}
		previous.ParsedBinaries = nil
		if err := c.recordRollbackHistory(previous); err != nil {
			t.Fatal(err)
		}
		enrolled := current
		if !intact {
			enrolled.ParsedLayers = []string{replacedTopLayer}
		}
		if enrolment := c.EnrolApplication(enrolled); enrolment.Outcome != EnrolmentRecorded {
			t.Fatal(enrolment.Reason)
		}
		seedApplication(t, c, current)
		if _, err := c.Rollback(current.Origin); err != nil {
			t.Fatal(err)
		}
		if c.launchRecognised(previous) != intact {
			t.Fatalf("rollback of an installation recognised=%v moved the anchor wrongly", intact)
		}
	}
}

func TestRuntimeBranchesDoNotShareAnAnchor(t *testing.T) {
	c := newTestCpak(t)
	authority := useEnrolmentAuthority(t)
	first := enrolledApplication(t, c)
	first.Origin = FlatpakOrigin("flathub", "org.example.Platform")
	second := first
	second.CpakId, second.Version, second.Branch = "second", "2.0", "next"
	seedApplication(t, c, first)
	c.enrolFlatpakRuntime(first)
	if _, held := authority.holds(t, first.Origin); !held {
		t.Fatal("a runtime with one branch was not enrolled")
	}
	seedApplication(t, c, second)
	c.enrolFlatpakRuntime(second)
	if _, held := authority.holds(t, first.Origin); held {
		t.Fatal("one anchor was left answering for two branches")
	}
}

func TestRuntimeUpdateFindsOnlyTheRecognisedDependents(t *testing.T) {
	c := newTestCpak(t)
	useEnrolmentAuthority(t)
	runtime := enrolledApplication(t, c)
	runtime.Origin = FlatpakOrigin("flathub", "org.example.Platform")
	runtime.CpakId = "runtime"
	seedApplication(t, c, runtime)
	app := verifiedApplication()
	app.ParsedDependencies = []types.Dependency{{Id: runtime.CpakId, Origin: runtime.Origin}}
	seedApplication(t, c, app)
	found, err := c.recognisedDependents(runtime)
	if err != nil || len(found) != 0 {
		t.Fatalf("an unrecognised dependent would be enrolled: %v %v", found, err)
	}
	if enrolment := c.EnrolApplication(app); enrolment.Outcome != EnrolmentRecorded {
		t.Fatal(enrolment.Reason)
	}
	if found, err = c.recognisedDependents(runtime); err != nil || len(found) != 1 {
		t.Fatalf("a recognised dependent was lost: %v %v", found, err)
	}
}

func TestFailedFlatpakUpdateKeepsTheInstalledAnchor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	useEnrolmentAuthority(t)
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
	installed, err := c.getStoredApplication(plan.Origin, "stable", "stable", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.launchRecognised(installed) {
		t.Fatal("the installation was not enrolled")
	}
	repo.Commit("app/org.example.App/x86_64/stable", map[string]ostreetest.File{
		"files/bin/example": {Content: []byte("#!/bin/sh\nexit 0\n"), Mode: 0o755},
	}, map[string]gvariant.Variant{"xa.metadata": {Signature: "s", Value: "[Application]\nname=org.example.App\nruntime=org.example.Platform/x86_64/1\ncommand=example\n\n[Context]\nshared=network;ipc;\n"}})
	deps := c.newUpdateDeps()
	deps.createExports = func(next types.Application) error {
		if next.CpakId == installed.CpakId && sameInstallation(next, installed) {
			return nil
		}
		c.EnrolPublishedApplication(next, PublishedPackage{})
		return errors.New("exports failed")
	}
	if result := c.updateFlatpakApplication(installed, deps, nil); result.Status != types.UpdateStatusFailed {
		t.Fatalf("the update did not fail: %+v", result)
	}
	if !c.launchRecognised(installed) {
		t.Fatal("a failed update left the installation without its anchor")
	}
}

func TestRemovingAFailedInstallationReturnsTheAnchor(t *testing.T) {
	c := newTestCpak(t)
	useEnrolmentAuthority(t)
	kept := enrolledApplication(t, c)
	kept.ParsedBinaries = nil
	seedApplication(t, c, kept)
	bindLayer(t, c, replacedTopLayer, "state-next")
	failed := kept
	failed.CpakId, failed.Version, failed.Branch = "failed", "2.0", "next"
	failed.ParsedLayers = []string{verifiedBaseLayer, replacedTopLayer}
	seedApplication(t, c, failed)
	if enrolment := c.EnrolApplication(failed); enrolment.Outcome != EnrolmentRecorded {
		t.Fatal(enrolment.Reason)
	}
	if err := c.Remove(failed.Origin, failed.Branch, "", ""); err != nil {
		t.Fatal(err)
	}
	if !c.launchRecognised(kept) {
		t.Fatal("the remaining installation was left with the anchor of a failed one")
	}
}

func TestAuditTellsTheBranchesOfOneOriginApart(t *testing.T) {
	c := newTestCpak(t)
	useEnrolmentAuthority(t)
	first := enrolledApplication(t, c)
	first.ParsedBinaries = nil
	seedApplication(t, c, first)
	second := first
	second.CpakId, second.Version, second.Branch = "second", "2.0", "next"
	seedApplication(t, c, second)
	states, err := c.AnchorStates()
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]bool{}
	for _, state := range states {
		versions[state.Version] = true
	}
	if len(versions) != 2 || !versions[first.Version] || !versions[second.Version] {
		t.Fatalf("the audit cannot tell the branches apart: %+v", states)
	}
}
