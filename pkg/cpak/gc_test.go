package cpak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

func TestCollectGarbageKeepsReferencedLayers(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	for _, layer := range []string{"used", "orphan", "stale.partial"} {
		if err := os.MkdirAll(c.GetInStoreDir("layers", layer), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(c.Options.CachePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Options.CachePath, "download.partial"), []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.collectGarbage([]types.Application{{ParsedLayers: []string{"used"}}}, true); err != nil {
		t.Fatalf("garbage collection failed: %v", err)
	}
	if _, err := os.Stat(c.GetInStoreDir("layers", "used")); err != nil {
		t.Fatalf("referenced layer removed: %v", err)
	}
	if _, err := os.Stat(c.fvsLayerPath("used")); !os.IsNotExist(err) {
		t.Fatalf("garbage collection migrated a referenced layer: %v", err)
	}
	for _, path := range []string{c.GetInStoreDir("layers", "orphan"), c.GetInStoreDir("layers", "stale.partial"), filepath.Join(c.Options.CachePath, "download.partial")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected garbage to be removed: %s", path)
		}
	}
}

func TestCollectGarbageRemovesOrphanedLayerRecords(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	layer := strings.Repeat("c", 64)
	bindings, err := c.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	if err = bindings.Bind(integrity.LayerBinding{
		OCIDigest: layer,
		StateID:   "state",
		StateRoot: "root",
	}); err != nil {
		t.Fatal(err)
	}
	binding := c.GetInStoreDir("bindings", layer+".json")
	checkout := c.GetInStoreDir("bindings", layer+".checkout.json")
	if err = os.WriteFile(checkout, []byte("record"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err = c.collectGarbageReport(nil, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{binding, checkout} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("dry run removed %s: %v", path, statErr)
		}
	}
	if _, err = c.collectGarbageReport(nil, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{binding, checkout} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("orphaned layer record remained: %s", path)
		}
	}
}

func TestCollectGarbageRemovesReferencedLegacyLayerWhenFVSIsAvailable(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	legacy := c.GetInStoreDir("layers", "used")
	storage, err := store.Open(c.daBaDeeStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Import(context.Background(), filepath.Join(legacy, "value"), strings.NewReader("legacy"), store.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	seedFVSLayerFile(t, c, "used", "value", []byte("current"))

	report, err := c.collectGarbageReport([]types.Application{{ParsedLayers: []string{"used"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Layers) != 1 || report.Layers[0].Path != legacy {
		t.Fatalf("dry-run report = %+v", report)
	}
	if report.LegacyObjects != 1 || report.LegacyBytes != 6 {
		t.Fatalf("legacy dry-run report = %+v", report)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("dry run removed legacy layer: %v", err)
	}

	report, err = c.collectGarbageReport([]types.Application{{ParsedLayers: []string{"used"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Layers) != 1 || report.Layers[0].Path != legacy {
		t.Fatalf("applied report = %+v", report)
	}
	if report.LegacyObjects != 1 || report.LegacyBytes != 6 {
		t.Fatalf("legacy applied report = %+v", report)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy layer remained: %v", err)
	}
	if available, err := c.fvsLayerAvailable("used"); err != nil || !available {
		t.Fatalf("FVS layer changed: available=%t err=%v", available, err)
	}
}

func TestCollectGarbageKeepsReferencedLegacyLayerWhenFVSIsIncomplete(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	legacy := c.GetInStoreDir("layers", "used")
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "value"), []byte("legacy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.fvsLayerPath("used"), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := c.collectGarbageReport([]types.Application{{ParsedLayers: []string{"used"}}}, true); err == nil {
		t.Fatal("incomplete FVS layer was accepted")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy layer changed: %v", err)
	}
}

func TestCollectGarbageReportsBeforeApplying(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	orphan := c.GetInStoreDir("layers", "orphan")
	cached := filepath.Join(c.Options.CachePath, "download")
	if err := os.MkdirAll(orphan, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Options.CachePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "data"), []byte("layer"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("cache"), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := c.collectGarbageReport(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Applied || len(report.Layers) != 1 || len(report.Cache) != 1 || report.Bytes < 10 {
		t.Fatalf("unexpected dry-run report: %+v", report)
	}
	for _, path := range []string{orphan, cached} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run removed %s: %v", path, err)
		}
	}
	report, err = c.collectGarbageReport(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Fatal("apply report is not marked as applied")
	}
	for _, path := range []string{orphan, cached} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("apply kept %s: %v", path, err)
		}
	}
}

func TestCollectGarbageRemovesUnlinkedDaBaDeeObjects(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	layer := c.GetInStoreDir("layers", "orphan")
	storage, err := store.Open(c.daBaDeeStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Import(context.Background(), filepath.Join(layer, "value"), strings.NewReader("unused"), store.ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := c.collectGarbageReport(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Objects != 1 || report.ObjectBytes != 6 || report.LegacyObjects != 1 || report.LegacyBytes != 6 {
		t.Fatalf("object store report: %+v", report)
	}
	if _, err = os.Stat(filepath.Join(layer, "value")); err != nil {
		t.Fatalf("dry run removed layer: %v", err)
	}
	report, err = c.collectGarbageReport(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.Objects != 1 || report.ObjectBytes != 6 || report.LegacyObjects != 1 || report.LegacyBytes != 6 {
		t.Fatalf("applied object store report: %+v", report)
	}
	storage, err = store.Open(c.daBaDeeStoreOptions())
	if err != nil {
		t.Fatal(err)
	}
	dryRun, err := storage.CollectGarbage(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Objects != 0 {
		t.Fatalf("object remained after gc: %+v", dryRun)
	}
	if err = storage.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectGarbageRemovesFVSBlocksAfterLastLayer(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	seedFVSLayerFile(t, c, "orphan", "value", []byte("unused"))

	report, err := c.collectGarbageReport(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Layers) != 1 || report.FVSBlocks != 0 {
		t.Fatalf("dry-run report = %+v", report)
	}
	report, err = c.collectGarbageReport(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.FVSBlocks == 0 || report.ObjectBytes == 0 {
		t.Fatalf("applied report = %+v", report)
	}
	if _, err := os.Stat(c.fvsLayerPath("orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan repository remained: %v", err)
	}
	entries, err := os.ReadDir(c.fvsBlocksPath())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("block store still contains %d entries", len(entries))
	}
}

func TestCollectGarbageRemovesUnreferencedDriverCheckout(t *testing.T) {
	c := newTestCpak(t)
	c.Options.CachePath = filepath.Join(t.TempDir(), "cache")
	handler, err := storaged.NewFVS(c.fvsRoot(), c.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	c.storageDriver = handler
	seedFVSLayerFile(t, c, "used", "value", []byte("used"))
	seedFVSLayerFile(t, c, "orphan", "value", []byte("orphan"))
	if _, err := c.prepareStorageDriver([]string{"used", "orphan"}); err != nil {
		t.Fatal(err)
	}
	report, err := c.collectGarbageReport([]types.Application{{ParsedLayers: []string{"used"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.DriverLayers != 1 {
		t.Fatalf("dry-run report = %+v", report)
	}
	if _, err := os.Stat(c.fvsCheckoutPath("orphan")); err != nil {
		t.Fatalf("dry run removed checkout: %v", err)
	}
	report, err = c.collectGarbageReport([]types.Application{{ParsedLayers: []string{"used"}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if report.DriverLayers != 1 {
		t.Fatalf("applied report = %+v", report)
	}
	if _, err := os.Stat(c.fvsCheckoutPath("orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan checkout remained: %v", err)
	}
	if _, err := c.preparedLayerDirectories([]string{"orphan"}); !errors.Is(err, errStoragePreparationRequired) {
		t.Fatalf("orphan index entry remained: %v", err)
	}
}
