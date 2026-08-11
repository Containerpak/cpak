package cpak

import (
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	_, err := DecodeManifest([]byte(`{"manifest_version":"2.0","name":"Demo","description":"Demo application","version":"1.0.0","image":"ghcr.io/containerpak/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","binaries":["/usr/bin/demo"],"unknown":true}`))
	if err == nil {
		t.Fatal("expected unknown manifest field to fail")
	}
}

func TestDecodeManifestDefaultsToVersionOne(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"name":"Demo","description":"Demo application","version":"1.0.0","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"]}`))
	if err != nil {
		t.Fatalf("failed to decode legacy manifest: %v", err)
	}
	if manifest.ManifestVersion != "1.0" {
		t.Fatalf("expected legacy manifest version, got %q", manifest.ManifestVersion)
	}
}

func TestValidateManifestRejectsLegacyFilesystemFieldsInVersionTwo(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"manifest_version":"2.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"],"override":{"fsHostHome":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted legacy filesystem field")
	}
}

func TestValidateManifestRejectsFilesystemInVersionOne(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"manifest_version":"1.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"],"override":{"filesystem":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted v2 filesystem field in v1 manifest")
	}
}

func TestValidateManifestAcceptsFilesystemPermissions(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}, {Path: "host", Access: "read-only"}, {Path: "/etc/machine-id", Access: "read-only"}}
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
}

func TestValidateManifestRejectsWritableHostScope(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.Filesystem = []types.FilesystemPermission{{Path: "host", Access: "read-write"}}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted writable host scope")
	}
}

func TestMigrateManifestPreservesV1Permissions(t *testing.T) {
	override := types.NewOverride()
	override.FsHostEtc = true
	override.FsHostHome = true
	override.FsExtra = []string{"/srv/data"}
	manifest := &types.CpakManifest{ManifestVersion: "1.0", Override: override}
	if err := MigrateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestVersion != "2.0" || !manifest.Override.Network || manifest.Override.FsHostHome {
		t.Fatalf("v1 permissions were not preserved: %+v", manifest)
	}
	want := []types.FilesystemPermission{
		{Path: "/etc", Access: "read-only"},
		{Path: "home", Access: "read-write"},
		{Path: "/srv/data", Access: "read-write"},
	}
	if !reflect.DeepEqual(manifest.Override.Filesystem, want) {
		t.Fatalf("filesystem migration: got %v, want %v", manifest.Override.Filesystem, want)
	}
}

func TestMigrateManifestRejectsWritableHostRoot(t *testing.T) {
	manifest := &types.CpakManifest{
		ManifestVersion: "1.0",
		Override:        types.Override{FsExtra: []string{"/"}},
	}
	if err := MigrateManifest(manifest); err == nil {
		t.Fatal("migrated a writable host root grant")
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
