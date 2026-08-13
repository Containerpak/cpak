package cpak

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
