package cpak

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
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
	for _, path := range []string{c.GetInStoreDir("layers", "orphan"), c.GetInStoreDir("layers", "stale.partial"), filepath.Join(c.Options.CachePath, "download.partial")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected garbage to be removed: %s", path)
		}
	}
}
