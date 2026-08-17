package cpak

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func legacyStore(t *testing.T, layers ...string) Cpak {
	t.Helper()
	root := t.TempDir()
	for _, layer := range layers {
		if err := os.MkdirAll(filepath.Join(root, "layers", layer), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return Cpak{Options: types.CpakOptions{StorePath: root}}
}

func TestLegacyLayerDirectoriesUseOverlayPriority(t *testing.T) {
	c := legacyStore(t, "base", "top", "runtime")
	got, err := c.legacyLayerDirectories([]string{"base", "top", "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	root := c.Options.StorePath
	want := []string{
		filepath.Join(root, "layers", "runtime"),
		filepath.Join(root, "layers", "top"),
		filepath.Join(root, "layers", "base"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected overlay order: got %v, want %v", got, want)
	}
}

// A legacy layout used to reach the caller as a success carrying no directories,
// which left the spawn side to rebuild the paths from an argument nobody had
// checked. The directories have to come back with the success.
func TestLegacyLayoutReturnsItsDirectories(t *testing.T) {
	c := legacyStore(t, "base", "top")
	_, lowerDirs, _, err := c.prepareLayerMount(t.TempDir(), []string{"base", "top"})
	if err != nil {
		t.Fatal(err)
	}
	if lowerDirs == "" {
		t.Fatal("the legacy layout reported success without any layer directory")
	}
	for _, layer := range []string{"base", "top"} {
		if !strings.Contains(lowerDirs, filepath.Join(c.Options.StorePath, "layers", layer)) {
			t.Fatalf("%s is missing from %s", layer, lowerDirs)
		}
	}
}
