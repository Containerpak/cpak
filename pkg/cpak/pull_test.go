package cpak

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLayerAvailableRequiresExactDirectory(t *testing.T) {
	c := newTestCpak(t)
	if err := os.MkdirAll(c.GetInStoreDir("layers", "abc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.GetInStoreDir("layers", "abc.partial-old"), 0755); err != nil {
		t.Fatal(err)
	}
	available, err := c.layerAvailable("abc")
	if err != nil || !available {
		t.Fatalf("expected exact layer directory to be available: %v", err)
	}
	available, err = c.layerAvailable("ab")
	if err != nil || available {
		t.Fatalf("partial digest must not match: available=%v err=%v", available, err)
	}
	if _, err := os.Stat(filepath.Join(c.Options.StorePath, "layers", "abc.partial-old")); err != nil {
		t.Fatal(err)
	}
}
