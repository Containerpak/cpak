package cpak

import (
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
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

func TestUnpackImageLayersReturnsExistingLayers(t *testing.T) {
	c := newTestCpak(t)
	layer := static.NewLayer([]byte("existing layer"), types.OCIUncompressedLayer)
	digest, err := layer.Digest()
	if err != nil {
		t.Fatal(err)
	}
	layerID := digest.Hex
	if err = os.MkdirAll(c.GetInStoreDir("layers", layerID), 0755); err != nil {
		t.Fatal(err)
	}

	layers, err := c.unpackImageLayers("test", nil, []v1.Layer{layer})
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 1 || layers[0] != layerID {
		t.Fatalf("existing layer missing from result: %v", layers)
	}
}
