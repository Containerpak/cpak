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
	// A v1 manifest carried every key, so one that wanted the network said so.
	override.Network = true
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

func TestMigrateManifestConvertsKnownHostCommandShims(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.AllowedHostCommands = []string{"notify-send", "xdg-open", "cpak-launch-app"}
	if err := MigrateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Override.Notification || !manifest.Override.OpenURI || !manifest.Override.HostApplications {
		t.Fatalf("legacy shims were not converted: %+v", manifest.Override)
	}
	if len(manifest.Override.AllowedHostCommands) != 0 {
		t.Fatalf("legacy shims remain after migration: %v", manifest.Override.AllowedHostCommands)
	}
}

func TestValidateManifestRejectsUnknownLegacyHostCommand(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.AllowedHostCommands = []string{"sh"}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("unknown legacy host command was accepted")
	}
}

func TestValidateManifestAcceptsTypedContainerActions(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.HostActions = []types.HostActionGrant{{
		Provider:     types.HostActionProviderContainers,
		Capabilities: []string{types.HostActionContainersRead, types.HostActionContainersManageOwned},
	}}
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatal(err)
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

func TestValidateManifestAcceptsDependencyModes(t *testing.T) {
	for _, mode := range []string{"", "nested", "layer"} {
		manifest := validManifestForTest()
		manifest.Dependencies = []types.Dependency{{Origin: "github.com/example/component", Mode: mode}}
		if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
}

func TestValidateManifestRejectsUnknownDependencyMode(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Dependencies = []types.Dependency{{Origin: "github.com/example/component", Mode: "shared"}}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted an unknown dependency mode")
	}
}

func TestValidateManifestAcceptsLoginSession(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:          "dev.sinty.singularity",
		Name:        "Singularity",
		Description: "Singularity Desktop Environment",
		Kind:        "desktop",
		Entrypoint:  manifest.Binaries[0],
		Override:    types.Override{DeviceDri: true},
	}}
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
}

func TestValidateManifestRejectsSessionCommandSurface(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:         "dev.sinty.singularity",
		Name:       "Singularity",
		Kind:       "desktop",
		Entrypoint: manifest.Binaries[0],
		Override: types.Override{
			AllowedHostCommands: []string{"sh"},
		},
	}}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted a host command in a login session")
	}
}

func TestValidateManifestRejectsSessionEntrypointOutsideExports(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:         "dev.sinty.singularity",
		Name:       "Singularity",
		Kind:       "desktop",
		Entrypoint: "/bin/sh",
	}}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted an unexported session entrypoint")
	}
}

func TestValidateManifestRejectsSessionDesktopInjection(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Sessions = []types.Session{{
		ID:         "dev.sinty.singularity",
		Name:       "Singularity\nExec=/bin/sh",
		Kind:       "desktop",
		Entrypoint: manifest.Binaries[0],
	}}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("accepted a newline in a session name")
	}
}

// TestValidatingAV1ManifestLeavesEveryDigestAlone guards the boundary the
// migration must not cross. The lock records a package by the hash of its
// manifest and a publisher signs the same hash, both of them after validation,
// so a manifest that came back from validation migrated would name a package
// nobody published and no lock ever wrote.
func TestValidatingAV1ManifestLeavesEveryDigestAlone(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"manifest_version":"1.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"],"override":{"fsHostHome":true,"fsExtra":["/srv/data"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	published, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := manifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ManifestVersion != "1.0" || !manifest.Override.FsHostHome || len(manifest.Override.FsExtra) != 1 {
		t.Fatalf("validation rewrote the manifest: %s %+v", manifest.ManifestVersion, manifest.Override)
	}
	validated, err := manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if validated != published {
		t.Fatalf("the lock digest moved across validation: %s became %s", published, validated)
	}
	validatedIdentity, err := manifestIdentityDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if validatedIdentity != identity {
		t.Fatalf("the identity digest moved across validation: %s became %s", identity, validatedIdentity)
	}
}

// TestInstalledOverrideMigratesWithoutTouchingTheManifest is the other half:
// the legacy fields do stop reaching an installation, they just stop doing it
// through the manifest.
func TestInstalledOverrideMigratesWithoutTouchingTheManifest(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"manifest_version":"1.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"],"override":{"fsHostHome":true,"fsExtra":["/srv/data"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = (&Cpak{}).ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	override, err := installedOverride(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := []types.FilesystemPermission{
		{Path: "home", Access: "read-write"},
		{Path: "/srv/data", Access: "read-write"},
	}
	if !reflect.DeepEqual(override.Filesystem, want) {
		t.Fatalf("migrated grants: got %v, want %v", override.Filesystem, want)
	}
	if override.FsHostHome || len(override.FsExtra) != 0 {
		t.Fatalf("legacy fields survived into the installed override: %+v", override)
	}
	if manifest.ManifestVersion != "1.0" || !manifest.Override.FsHostHome {
		t.Fatalf("the manifest was migrated with it: %s %+v", manifest.ManifestVersion, manifest.Override)
	}
}

// TestMigratingAManifestThatGrantedNothingChangesNothing keeps an update of a
// v1 package from reporting a permission change it does not make: Override.Diff
// compares the fields whole, and an empty grant list is not the absent one.
func TestMigratingAManifestThatGrantedNothingChangesNothing(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{"manifest_version":"1.0","name":"Demo","description":"Demo application","image":"ghcr.io/containerpak/demo:latest","binaries":["/usr/bin/demo"]}`))
	if err != nil {
		t.Fatal(err)
	}
	override, err := installedOverride(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if changes := manifest.Override.Diff(override); len(changes) != 0 {
		t.Fatalf("migrating a manifest that granted nothing reported %v", changes)
	}
}
