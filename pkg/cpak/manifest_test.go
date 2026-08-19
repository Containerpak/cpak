package cpak

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/tools"
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

// The two spellings of one control. ESC [ is the seven-bit form; U+009B is CSI
// itself, it arrives through a JSON manifest as an ordinary escaped character
// where a raw ESC byte would be refused by the decoder, and VTE and xterm act
// on it in UTF-8 mode. A rule that stops at 0x7f refuses the first and lets the
// second through, which is the same attack in a second encoding.
var manifestRedraws = map[string]string{
	"escape": "\x1b[1A\x1b[2K",
	"csi":    "\u009b1A\u009b2K",
}

// A manifest is printed to the reader, in full, before it is validated, so the
// prompt escapes what it writes. This is the other half: a manifest that
// carries a cursor movement at all is refused, so nothing downstream has to
// remember. Each field is validated twice, once clean and once tainted, so a
// refusal below is known to be caused by the sequence and not by the rest of
// the manifest being unacceptable.
func TestValidateManifestRefusesTerminalControlInPublisherText(t *testing.T) {
	fields := map[string]func(*types.CpakManifest, string){
		"name":        func(m *types.CpakManifest, taint string) { m.Name = "demo" + taint },
		"description": func(m *types.CpakManifest, taint string) { m.Description = "a demo application" + taint },
		"binary": func(m *types.CpakManifest, taint string) {
			m.Binaries = []string{"/usr/bin/demo" + taint}
		},
		"desktop entry": func(m *types.CpakManifest, taint string) {
			m.DesktopEntries = []string{"/usr/share/applications/demo" + taint + ".desktop"}
		},
		"dependency origin": func(m *types.CpakManifest, taint string) {
			m.Dependencies = []types.Dependency{{Origin: "github.com/example/demo" + taint, Mode: "nested"}}
		},
		"dependency branch": func(m *types.CpakManifest, taint string) {
			m.Dependencies = []types.Dependency{{Origin: "github.com/example/demo", Branch: "main" + taint, Mode: "nested"}}
		},
		"runtime source url": func(m *types.CpakManifest, taint string) {
			m.RuntimeSources = []types.RuntimeSource{{
				URL:       "https://example.org/demo.deb" + taint,
				SHA256:    strings.Repeat("a", 64),
				Size:      4096,
				Installer: "dpkg",
			}}
		},
		"addon": func(m *types.CpakManifest, taint string) { m.Addons = []string{"github.com/example/addon" + taint} },
		"environment": func(m *types.CpakManifest, taint string) {
			m.Override.Env = []string{"HOME=/home/demo" + taint}
		},
		"filesystem path": func(m *types.CpakManifest, taint string) {
			m.Override.Filesystem = []types.FilesystemPermission{{Path: "/srv/demo" + taint, Access: "read-only"}}
		},
		"session name": func(m *types.CpakManifest, taint string) {
			m.Sessions = []types.Session{sessionForText(m, "Demo"+taint, "")}
		},
		"session environment": func(m *types.CpakManifest, taint string) {
			m.Sessions = []types.Session{sessionForText(m, "Demo", "HOME=/home/demo"+taint)}
		},
	}

	for field, apply := range fields {
		clean := validManifestForTest()
		apply(clean, "")
		if err := (&Cpak{}).ValidateManifest(clean); err != nil {
			t.Errorf("the %s case is not otherwise valid, so it proves nothing: %v", field, err)
			continue
		}
		for spelling, redraw := range manifestRedraws {
			manifest := validManifestForTest()
			apply(manifest, redraw)
			if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
				t.Errorf("accepted a %s carrying the %s form of a cursor movement", field, spelling)
			}
		}
	}
}

// A description is the one manifest string that is prose, and prose has
// paragraphs, indentation and length. ValidateManifest runs on install,
// update, lock and verify alike, so refusing an ordinary description does not
// merely reject a new package: it makes an already installed one impossible to
// update and impossible to reinstall. Every value here comes from a manifest
// published today, except the last, which is what the limit is for.
func TestValidateManifestAcceptsTheDescriptionsPackagesActuallyCarry(t *testing.T) {
	for name, description := range map[string]string{
		"one line":                "Kate is a multi-document editor part of KDE since release 2.2",
		"a paragraph and a list":  "A demo application.\n\nIt does one thing:\n\tit demonstrates.",
		"an accented word":        "Un \u00e9diteur de texte l\u00e9ger et rapide",
		"an emoji":                "Screen recorder \U0001f3a5 for GNOME",
		"as long as the rule now": strings.Repeat("a", 4096),
	} {
		t.Run(name, func(t *testing.T) {
			manifest := validManifestForTest()
			manifest.Description = description
			if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
				t.Fatalf("an ordinary description was refused: %v", err)
			}
		})
	}

	manifest := validManifestForTest()
	manifest.Description = strings.Repeat("a", 4097)
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("a description past the limit was accepted")
	}

	// A carriage return is the one whitespace character prose does not get.
	// It returns the cursor to the start of the line that was just written,
	// which is half a redraw on its own, and no manifest in the store carries
	// one. A description written on Windows has to lose its \r.
	manifest = validManifestForTest()
	manifest.Description = "A demo application.\r\nWritten on another machine."
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("a carriage return in a description was accepted")
	}
}

func sessionForText(manifest *types.CpakManifest, name, environment string) types.Session {
	session := types.Session{
		ID:         "org.example.demo",
		Name:       name,
		Kind:       "desktop",
		Entrypoint: manifest.Binaries[0],
	}
	if environment != "" {
		session.Override.Env = []string{environment}
	}
	return session
}

// The route the sequence actually takes. A manifest fetched from a repository
// is JSON, and the decoder cpak really uses rejects a raw ESC byte but accepts
// the escaped form of CSI without complaint, so the refusal has to hold on what
// comes out of the decoder and not only on a struct built in a test.
func TestValidateManifestRefusesADecodedCursorMovement(t *testing.T) {
	content := []byte(`{"manifest_version":"2.0","name":"Demo\u009b1A\u009b2K","description":"a demo application","image":"ghcr.io/example/demo:latest","binaries":["/usr/bin/demo"]}`)
	manifest, err := DecodeManifest(content)
	if err != nil {
		t.Fatalf("the decoder refused the manifest before validation could: %v", err)
	}
	if !strings.ContainsRune(manifest.Name, 0x9b) {
		t.Fatalf("the decoded name does not carry CSI, so the case proves nothing: %q", manifest.Name)
	}
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("a manifest that decoded to a cursor movement was accepted")
	}
}

// The validator refuses a character and the printer escapes it, and the two
// live in different packages. They were written from one rule and there is
// nothing but this test to keep them on it: a printer that escapes less than
// the validator refuses would put a live control character on the terminal of
// anyone reading a manifest that has not been validated yet, which is every
// reader of the install prompt.
func TestTheValidatorRefusesWhatThePrinterEscapes(t *testing.T) {
	for character := rune(0); character <= 0x00a0; character++ {
		value := "demo" + string(character) + "demo"
		manifest := validManifestForTest()
		manifest.Name = value
		refused := (&Cpak{}).ValidateManifest(manifest) != nil
		escaped := tools.SanitizeForDisplay(value) != value
		if refused != escaped {
			t.Errorf("U+%04X is refused=%v by the validator and escaped=%v by the printer", character, refused, escaped)
		}
	}
}
