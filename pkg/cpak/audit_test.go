/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	auditedLayer      = "1111111111111111111111111111111111111111111111111111111111111111"
	auditedOtherLayer = "2222222222222222222222222222222222222222222222222222222222222222"
	auditedOrigin     = "github.com/user/audited"
	auditedBaseOrigin = "github.com/user/audited-base"
)

func auditedApplication(origin, id string, layers ...string) types.Application {
	return types.Application{
		CpakId:       id,
		Name:         "audited",
		Version:      "1.0",
		Branch:       "main",
		Origin:       origin,
		ImageDigest:  "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		Config:       `{"config":{"Env":["PATH=/usr/bin"]}}`,
		ParsedLayers: layers,
	}
}

// auditedReportFor answers with the entry the report holds for one origin, so
// a test never depends on the order the store hands its applications back in.
func auditedReportFor(t *testing.T, report StoreIntegrity, origin string) ApplicationIntegrity {
	t.Helper()
	for _, app := range report.Applications {
		if app.Origin == origin {
			return app
		}
	}
	t.Fatalf("the report says nothing about %s: %+v", origin, report.Applications)
	return ApplicationIntegrity{}
}

// auditedPreparedLayer puts a layer through the storage driver the way an
// install does, optionally binding it first, and answers with the directory the
// driver prepared.
func auditedPreparedLayer(t *testing.T, cp *Cpak, digest string, bind bool) string {
	t.Helper()
	if cp.storageDriver == nil {
		handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
		if err != nil {
			t.Fatal(err)
		}
		cp.storageDriver = handler
	}
	seedFVSLayerFile(t, cp, digest, "usr/share/value", []byte("value"))
	if bind {
		if err := cp.recordLayerBinding(digest); err != nil {
			t.Fatalf("the layer binding was refused: %v", err)
		}
	}
	paths, err := cp.prepareStorageDriver([]string{digest})
	if err != nil {
		t.Fatalf("the layer could not be prepared: %v", err)
	}
	return paths[0]
}

// auditedRebuiltLayer throws a layer repository away and commits another one in
// its place, leaving the binding filed for it untouched.
func auditedRebuiltLayer(t *testing.T, cp *Cpak, digest string, content []byte) {
	t.Helper()
	if err := os.RemoveAll(cp.fvsLayerPath(digest)); err != nil {
		t.Fatal(err)
	}
	seedFVSLayerFile(t, cp, digest, "usr/share/value", content)
}

func TestIntegrityReportCountsTheBindingsAStoreHolds(t *testing.T) {
	cp := newTestCpak(t)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer, auditedOtherLayer))
	bindLayer(t, cp, auditedLayer, "state-one")

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if app.Layers != 2 || app.BoundLayers != 1 {
		t.Fatalf("got %d of %d layers bound, want 1 of 2", app.BoundLayers, app.Layers)
	}
	if report.UnboundLayers() != 1 {
		t.Fatalf("got %d unbound layers across the store, want 1", report.UnboundLayers())
	}
	if app.FullyAnswered() {
		t.Fatal("an application with an unbound layer was reported as fully answered for")
	}
}

// The store this exists for is the one made before any of these records did.
// Reporting on it must not be the thing that gives it a ledger.
func TestIntegrityReportWritesNothingToAStoreThatHasNoRecords(t *testing.T) {
	cp := newTestCpak(t)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if app.BoundLayers != 0 || app.Layers != 1 {
		t.Fatalf("got %d of %d layers bound, want 0 of 1", app.BoundLayers, app.Layers)
	}
	records := cp.GetInStoreDir("bindings")
	if _, err := os.Stat(records); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reporting created %s, so the report is not read only: %v", records, err)
	}
}

func TestIntegrityReportNamesALayerThatChangedUnderItsBinding(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, auditedLayer, "usr/share/value", []byte("value"))
	if err := cp.recordLayerBinding(auditedLayer); err != nil {
		t.Fatalf("the layer binding was refused: %v", err)
	}
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))
	auditedRebuiltLayer(t, cp, auditedLayer, []byte("another value"))

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	if report.Disagreements() != 1 {
		t.Fatalf("got %d disagreements, want the rebuilt layer to be one", report.Disagreements())
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if !strings.Contains(app.Disagreements[0], auditedLayer) {
		t.Fatalf("the disagreement does not name the layer that changed: %q", app.Disagreements[0])
	}
}

// A launch stacks the layers of its layer dependencies too, so an audit that
// counted only the layers an application declares would report a store as
// answered for while the layers it actually mounts were not.
func TestIntegrityReportCountsTheLayersALaunchComposes(t *testing.T) {
	cp := newTestCpak(t)
	seedApplication(t, cp, auditedApplication(auditedBaseOrigin, "audited-base", auditedOtherLayer))
	app := auditedApplication(auditedOrigin, "audited", auditedLayer)
	app.ParsedDependencies = []types.Dependency{{Id: "audited-base", Origin: auditedBaseOrigin, Mode: "layer"}}
	seedApplication(t, cp, app)
	bindLayer(t, cp, auditedLayer, "state-one")

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	composed := auditedReportFor(t, report, auditedOrigin)
	if composed.Layers != 2 {
		t.Fatalf("got %d layers, want the layer of the dependency to be counted as well", composed.Layers)
	}
	if composed.BoundLayers != 1 {
		t.Fatalf("got %d bound layers, want the unbound layer of the dependency to show", composed.BoundLayers)
	}
}

// An audit that stopped at the first application it could not read would say
// nothing about the ones after it, which is the opposite of what it is for.
func TestIntegrityReportKeepsGoingPastAnApplicationItCannotRead(t *testing.T) {
	cp := newTestCpak(t)
	broken := auditedApplication(auditedBaseOrigin, "audited-base", auditedOtherLayer)
	broken.ParsedDependencies = []types.Dependency{{Id: "never-installed", Origin: "github.com/user/missing", Mode: "layer"}}
	seedApplication(t, cp, broken)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))
	bindLayer(t, cp, auditedLayer, "state-one")

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("one unreadable application failed the whole report: %v", err)
	}
	if report.Unreadable() != 1 {
		t.Fatalf("got %d unreadable applications, want 1", report.Unreadable())
	}
	readable := auditedReportFor(t, report, auditedOrigin)
	if readable.Unreadable != "" || readable.BoundLayers != 1 {
		t.Fatalf("the application that could be read was not reported: %+v", readable)
	}
}

func TestIntegrityReportCountsThePreparedCheckoutsAStateDescribes(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	auditedPreparedLayer(t, cp, auditedLayer, true)
	auditedPreparedLayer(t, cp, auditedOtherLayer, false)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer, auditedOtherLayer))

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if app.PreparedCheckouts != 2 {
		t.Fatalf("got %d prepared checkouts, want both layers to be served from one", app.PreparedCheckouts)
	}
	if app.DescribedCheckouts != 1 {
		t.Fatalf("got %d described checkouts, want only the bound layer to be described", app.DescribedCheckouts)
	}
	if report.UndescribedCheckouts() != 1 {
		t.Fatalf("got %d undescribed checkouts across the store, want 1", report.UndescribedCheckouts())
	}
}

func TestIntegrityReportNamesAPreparedCheckoutThatChanged(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	checkout := auditedPreparedLayer(t, cp, auditedLayer, true)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))
	tamperWithCheckout(t, filepath.Join(checkout, "usr", "share", "value"))

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if len(app.Disagreements) != 1 || !strings.Contains(app.Disagreements[0], "prepared checkout") {
		t.Fatalf("the changed checkout was not reported: %v", app.Disagreements)
	}
}

// A launch that mounts the repositories through FUSE reads no prepared index,
// so a checkout left over in one is not what runs and must not be counted as
// something the store answers for.
func TestIntegrityReportCountsNoPreparedCheckoutUnderTheFUSEBackend(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	auditedPreparedLayer(t, cp, auditedLayer, true)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))

	t.Setenv("CPAK_STORAGE_BACKEND", "fuse")
	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if app.PreparedCheckouts != 0 || app.DescribedCheckouts != 0 {
		t.Fatalf("got %d of %d prepared checkouts counted, want none under FUSE", app.DescribedCheckouts, app.PreparedCheckouts)
	}
}

// What the report cannot see, said out loud. The shape a state gives a checkout
// covers the metadata of every entry and never the bytes in it, so content
// replaced by content of the same length reports as a store that agrees with
// itself. cpak storage verify and CheckPreparedCheckoutContents are where that
// is paid for on purpose; an audit is not, and this test exists so that a later
// reader knows it was a decision.
func TestIntegrityReportCallsAContentSwapThatKeepsTheShapeFullyAnswered(t *testing.T) {
	requireCheckoutResolve(t)
	cp := newTestCpak(t)
	checkout := auditedPreparedLayer(t, cp, auditedLayer, true)
	seedApplication(t, cp, auditedApplication(auditedOrigin, "audited", auditedLayer))

	value := filepath.Join(checkout, "usr", "share", "value")
	staged := value + ".swapped"
	if err := os.WriteFile(staged, []byte("VALUE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, value); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "VALUE" {
		t.Fatalf("the checkout holds %q, so nothing was swapped and this test proves nothing", content)
	}

	report, err := cp.IntegrityReport()
	if err != nil {
		t.Fatalf("the report could not be built: %v", err)
	}
	app := auditedReportFor(t, report, auditedOrigin)
	if !app.FullyAnswered() || len(app.Disagreements) != 0 {
		t.Fatalf("the swap was seen after all, so this test no longer documents the blind spot: %+v", app)
	}
}
