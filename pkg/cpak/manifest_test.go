package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	_, err := decodeManifest([]byte(`{"manifest_version":"2.0","name":"Demo","description":"Demo application","version":"1.0.0","image":"ghcr.io/containerpak/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","binaries":["/usr/bin/demo"],"unknown":true}`))
	if err == nil {
		t.Fatal("expected unknown manifest field to fail")
	}
}

func TestDecodeManifestDefaultsToVersionOne(t *testing.T) {
	manifest, err := decodeManifest([]byte(`{"name":"Demo","description":"Demo application","version":"1.0.0","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"]}`))
	if err != nil {
		t.Fatalf("failed to decode legacy manifest: %v", err)
	}
	if manifest.ManifestVersion != "1.0" {
		t.Fatalf("expected legacy manifest version, got %q", manifest.ManifestVersion)
	}
}

func TestValidateManifestSupportsDigestReference(t *testing.T) {
	c := &Cpak{}
	manifest := &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "Demo",
		Description:     "Demo application",
		Image:           "ghcr.io/containerpak/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Binaries:        []string{"/usr/bin/demo"},
	}
	if err := c.ValidateManifest(manifest); err != nil {
		t.Fatalf("failed to validate digest reference: %v", err)
	}
}

func TestValidateManifestRejectsUnknownVersion(t *testing.T) {
	err := (&Cpak{}).ValidateManifest(&types.CpakManifest{ManifestVersion: "3.0"})
	if err == nil {
		t.Fatal("expected unsupported version to fail")
	}
}
