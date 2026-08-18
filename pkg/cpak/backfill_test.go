/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	backfillLayer   = "4444444444444444444444444444444444444444444444444444444444444444"
	migratedLayer   = "5555555555555555555555555555555555555555555555555555555555555555"
	builtLayer      = "6666666666666666666666666666666666666666666666666666666666666666"
	unstoredLayer   = "7777777777777777777777777777777777777777777777777777777777777777"
	backfillOrigin  = "github.com/containerpak/backfill"
	backfillCpakId  = "backfill-app"
	secondaryCpakId = "backfill-other"
)

func seedLayerApplication(t *testing.T, cp *Cpak, cpakId string, layers ...string) {
	t.Helper()

	seedApplication(t, cp, types.Application{
		CpakId:       cpakId,
		Name:         "Backfill",
		Origin:       backfillOrigin,
		Branch:       "main",
		ParsedLayers: layers,
	})
}

func layerBinding(t *testing.T, cp *Cpak, digest string) (string, bool) {
	t.Helper()

	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding, found, err := bindings.Lookup(digest)
	if err != nil {
		t.Fatalf("the binding of %s could not be read: %v", digest, err)
	}
	return binding.StateRoot, found
}

func commitLayerState(t *testing.T, cp *Cpak, digest, name string, content []byte) {
	t.Helper()

	writer, err := fvsrepo.BeginSnapshot(cp.fvsLayerPath(digest), fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	entry := fvsrepo.Entry{Path: name, Kind: fvsrepo.EntryFile, Mode: 0o644, Size: int64(len(content))}
	if err = writer.Add(entry, strings.NewReader(string(content))); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillBindsALayerNoPullEverBound(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("payload"))
	seedLayerApplication(t, cp, backfillCpakId, backfillLayer)

	report, err := cp.BackfillLayerBindings()
	if err != nil {
		t.Fatalf("the backfill did not complete: %v", err)
	}
	if len(report.Bound) != 1 || report.Bound[0] != backfillLayer {
		t.Fatalf("a layer of an installed application was left unbound: %+v", report)
	}
	if len(report.Refused) != 0 || len(report.Unchanged) != 0 {
		t.Fatalf("the report does not describe what the pass did: %+v", report)
	}
	root, found := layerBinding(t, cp, backfillLayer)
	if !found {
		t.Fatal("the backfill reported a binding it did not write")
	}
	want, err := layerStateRoot(cp.fvsLayerPath(backfillLayer), layerState(t, cp, backfillLayer))
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatalf("got root %q, want the root of the state the store holds %q", root, want)
	}
}

// The record a pull wrote is the only one made at a moment anything was proven,
// so a backfill must never replace it.
func TestBackfillLeavesAnExistingRecordAlone(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(backfillLayer); err != nil {
		t.Fatal(err)
	}
	recorded, found := layerBinding(t, cp, backfillLayer)
	if !found {
		t.Fatal("the layer was not bound before the backfill ran")
	}
	commitLayerState(t, cp, backfillLayer, "usr/share/value", []byte("swapped"))
	seedLayerApplication(t, cp, backfillCpakId, backfillLayer)

	report, err := cp.BackfillLayerBindings()
	if err != nil {
		t.Fatalf("the backfill did not complete: %v", err)
	}
	if len(report.Bound) != 0 || len(report.Unchanged) != 1 || report.Unchanged[0] != backfillLayer {
		t.Fatalf("a bound layer was not reported as untouched: %+v", report)
	}
	root, _ := layerBinding(t, cp, backfillLayer)
	if root != recorded {
		t.Fatalf("the backfill rewrote a record made at pull time: got %q, want %q", root, recorded)
	}
}

// A backfill is trust on first use: it records the store as it stands, so a
// layer changed before it runs is recorded as changed. This test exists to keep
// that property visible, not to bless it.
func TestBackfillRecordsWhatIsOnDiskAndNotWhatWasPulled(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("payload"))
	pulled, err := layerStateRoot(cp.fvsLayerPath(backfillLayer), layerState(t, cp, backfillLayer))
	if err != nil {
		t.Fatal(err)
	}
	commitLayerState(t, cp, backfillLayer, "usr/share/value", []byte("swapped"))
	seedLayerApplication(t, cp, backfillCpakId, backfillLayer)

	if _, err = cp.BackfillLayerBindings(); err != nil {
		t.Fatalf("the backfill did not complete: %v", err)
	}
	root, found := layerBinding(t, cp, backfillLayer)
	if !found {
		t.Fatal("the layer was left unbound")
	}
	current, err := layerStateRoot(cp.fvsLayerPath(backfillLayer), layerState(t, cp, backfillLayer))
	if err != nil {
		t.Fatal(err)
	}
	if root != current {
		t.Fatalf("got root %q, want the root of the current state %q", root, current)
	}
	if root == pulled {
		t.Fatal("the changed layer was recorded as the state it held before it changed")
	}
}

func TestBackfillReportsALayerItCannotBind(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("payload"))
	seedLayerApplication(t, cp, backfillCpakId, unstoredLayer)
	seedLayerApplication(t, cp, secondaryCpakId, backfillLayer)

	report, err := cp.BackfillLayerBindings()
	if err != nil {
		t.Fatalf("one layer that cannot be bound stopped the whole pass: %v", err)
	}
	if len(report.Refused) != 1 || report.Refused[0].Layer != unstoredLayer {
		t.Fatalf("a layer that stayed unbound was not reported: %+v", report)
	}
	if report.Refused[0].Reason == "" {
		t.Fatal("a layer was refused without saying why")
	}
	if len(report.Bound) != 1 || report.Bound[0] != backfillLayer {
		t.Fatalf("a layer that could be bound was skipped: %+v", report)
	}
}

// A backfill answers for the layers of every installed application, because a
// layer dependency and an addon are installed applications of their own.
func TestBackfillWalksEveryInstalledApplication(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("payload"))
	seedFVSLayerFile(t, cp, builtLayer, "usr/share/other", []byte("component"))
	seedLayerApplication(t, cp, backfillCpakId, backfillLayer)
	seedLayerApplication(t, cp, secondaryCpakId, builtLayer, backfillLayer)

	report, err := cp.BackfillLayerBindings()
	if err != nil {
		t.Fatalf("the backfill did not complete: %v", err)
	}
	if len(report.Bound) != 2 {
		t.Fatalf("the layers of a second application were not bound: %+v", report)
	}
	for _, digest := range []string{backfillLayer, builtLayer} {
		if _, found := layerBinding(t, cp, digest); !found {
			t.Fatalf("layer %s was reported as bound and has no record", digest)
		}
	}
}

// A migrated layer is published without ever being downloaded under its digest,
// so the migration is the only place that can name the state it produced.
func TestMigratedLegacyLayerIsBound(t *testing.T) {
	cp := newTestCpak(t)
	legacy := cp.GetInStoreDir("layers", migratedLayer)
	if err := os.MkdirAll(filepath.Join(legacy, "usr", "share"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "usr", "share", "value"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	available, err := cp.ensureFVSLayer(migratedLayer)
	if err != nil || !available {
		t.Fatalf("available = %v, err = %v", available, err)
	}
	root, found := layerBinding(t, cp, migratedLayer)
	if !found {
		t.Fatal("a migrated layer was published without a binding")
	}
	want, err := layerStateRoot(cp.fvsLayerPath(migratedLayer), layerState(t, cp, migratedLayer))
	if err != nil {
		t.Fatal(err)
	}
	if root != want {
		t.Fatalf("got root %q, want the root of the migrated state %q", root, want)
	}
}

func TestBindBuiltLayersBindsOnlyWhatTheBuilderAdded(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, backfillLayer, "usr/share/value", []byte("pulled"))
	seedFVSLayerFile(t, cp, builtLayer, "usr/lib/locale/value", []byte("built"))

	if err := cp.bindBuiltLayers([]string{backfillLayer}, []string{backfillLayer, builtLayer}); err != nil {
		t.Fatalf("a built layer was not bound: %v", err)
	}
	if _, found := layerBinding(t, cp, builtLayer); !found {
		t.Fatal("the layer the builder added has no binding")
	}
	if _, found := layerBinding(t, cp, backfillLayer); found {
		t.Fatal("a layer the builder did not add was bound outside the pull that owns it")
	}
}

// A layer still held in the legacy directory layout has no state to name, and
// the migration binds it when it gets one, so an install is not failed here.
func TestBindBuiltLayersLeavesALayerWithNoStateUnbound(t *testing.T) {
	cp := newTestCpak(t)
	if err := cp.bindBuiltLayers(nil, []string{unstoredLayer}); err != nil {
		t.Fatalf("an install was failed by a layer that has nothing to bind: %v", err)
	}
	if _, found := layerBinding(t, cp, unstoredLayer); found {
		t.Fatal("a layer with no state was bound to one")
	}
}

// An application record written before ManifestDigest existed carries no such
// key, which is exactly what a record written now without a digest looks like.
func TestApplicationRecordWithoutManifestDigestStillLoads(t *testing.T) {
	cp := newTestCpak(t)
	seedLayerApplication(t, cp, backfillCpakId, backfillLayer)

	encoded, err := json.Marshal(types.Application{CpakId: backfillCpakId, Origin: backfillOrigin})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "manifest_digest") {
		t.Fatalf("a record without a manifest digest changed shape: %s", encoded)
	}
	apps := storedApplications(t, cp)
	if len(apps) != 1 {
		t.Fatalf("the store holds %d applications, want 1", len(apps))
	}
	if apps[0].CpakId != backfillCpakId {
		t.Fatalf("got application %q, want %q", apps[0].CpakId, backfillCpakId)
	}
	if apps[0].ManifestDigest != "" {
		t.Fatalf("a record that names no manifest loaded with the digest %q", apps[0].ManifestDigest)
	}
}

func TestManifestIdentityDigestFollowsTheManifest(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Backfill",
		Description:     "A manifest",
		Image:           "ghcr.io/containerpak/example:latest",
		Binaries:        []string{"/usr/bin/example"},
	}
	first, err := manifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("one manifest was named twice as %q and %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("the manifest digest does not name its algorithm: %q", first)
	}

	changed := *manifest
	changed.Image = "ghcr.io/containerpak/example:other"
	other, err := manifestIdentityDigest(&changed)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Fatal("two manifests that install different images share one digest")
	}
}
