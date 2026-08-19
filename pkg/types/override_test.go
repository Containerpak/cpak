package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOverrideDiffReportsChangedPermissions(t *testing.T) {
	before := NewOverride()
	after := before
	// Toggled rather than assigned, so the case reports a change whichever way
	// the default for the field goes.
	after.Network = !before.Network
	after.Filesystem = []FilesystemPermission{{Path: "/run/media", Access: "read-write"}}

	changes := before.Diff(after)
	expected := []string{"filesystem", "network"}
	if !reflect.DeepEqual(changes, expected) {
		t.Fatalf("expected %v, got %v", expected, changes)
	}
}

func TestOverrideAdditionsReportsOnlyNewPermissions(t *testing.T) {
	before := Override{Network: true, FsExtra: []string{"/one"}}
	after := Override{Network: true, DeviceDri: true, FsExtra: []string{"/one", "/two"}}
	additions := before.Additions(after)
	if len(additions) != 2 || additions[0] != "deviceDri" || additions[1] != "fsExtra" {
		t.Fatalf("unexpected additions: %v", additions)
	}
}

func TestOverrideAdditionsReportsFilePickerCapabilities(t *testing.T) {
	before := Override{FilePicker: FilePickerGrant{OpenFile: true}}
	after := before
	after.FilePicker.ContainingFolder = true

	additions := before.Additions(after)
	if len(additions) != 1 || additions[0] != "filePicker" {
		t.Fatalf("unexpected additions: %v", additions)
	}

	if additions = after.Additions(before); len(additions) != 0 {
		t.Fatalf("removing a file picker capability was reported as an addition: %v", additions)
	}
}

func TestDecodeFilePickerGrantJSON(t *testing.T) {
	grant, err := DecodeFilePickerGrantJSON([]byte(`{"openFile":true,"openFolder":true,"saveFile":true,"persistent":true,"containingFolder":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !grant.OpenFile || !grant.OpenFolder || !grant.SaveFile || !grant.Persistent || !grant.ContainingFolder {
		t.Fatalf("unexpected file picker grant: %+v", grant)
	}
}

func TestDecodeFilePickerGrantJSONRejectsUnknownFields(t *testing.T) {
	if _, err := DecodeFilePickerGrantJSON([]byte(`{"openFile":true,"unexpected":true}`)); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

// The whole point of the helper is telling false apart from absent, which the
// decoded struct cannot do, so that is what the case checks.
func TestUngrantedPermissionsTellsFalseApartFromAbsent(t *testing.T) {
	missing, err := UngrantedPermissions([]byte(`{"override":{"socketWayland":true,"socketX11":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, key := range missing {
		named[key] = true
	}
	for _, written := range []string{"socketWayland", "socketX11"} {
		if named[written] {
			t.Fatalf("%s is in the manifest and was reported as missing", written)
		}
	}
	if !named["socketSessionBus"] || !named["network"] {
		t.Fatalf("a permission the manifest never mentions was not reported: %v", missing)
	}
	// Absent by design, and reporting them would be noise on every manifest.
	for _, optional := range []string{"filesystem", "hostActions", "env", "fsExtra"} {
		if named[optional] {
			t.Fatalf("%s is omitted when empty and must not be reported", optional)
		}
	}
}

func TestAManifestThatNamesEveryPermissionHasNothingMissing(t *testing.T) {
	complete, err := json.Marshal(map[string]any{"override": NewOverride()})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := UngrantedPermissions(complete)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("a manifest naming every permission was told it omits %v", missing)
	}
}

// The legacy fields are one table with three readers: the migration that
// rewrites a v1 manifest, the ordering that decides whether a policy widens,
// and the update that weighs a stored override against a fetched one. This is
// that table. A policy carrying none of them comes back untouched, which is
// what keeps the reading out of every v2 policy's way.
func TestWithMigratedFilesystemIsTheOneTableTheLegacyFieldsMean(t *testing.T) {
	migrated := Override{
		SocketWayland: true,
		Filesystem:    []FilesystemPermission{{Path: "xdg-documents", Access: "read-only"}},
		FsHost:        true,
		FsHostEtc:     true,
		FsHostHome:    true,
		FsExtra:       []string{"/srv/data", "/srv/media"},
	}.WithMigratedFilesystem()

	want := []FilesystemPermission{
		{Path: "xdg-documents", Access: "read-only"},
		{Path: "host", Access: "read-only"},
		{Path: "/etc", Access: "read-only"},
		{Path: "home", Access: "read-write"},
		{Path: "/srv/data", Access: "read-write"},
		{Path: "/srv/media", Access: "read-write"},
	}
	if !reflect.DeepEqual(migrated.Filesystem, want) {
		t.Fatalf("migrated grants: got %v, want %v", migrated.Filesystem, want)
	}
	if migrated.FsHost || migrated.FsHostEtc || migrated.FsHostHome || migrated.FsExtra != nil {
		t.Fatalf("a legacy field survived the reading: %+v", migrated)
	}
	if !migrated.SocketWayland {
		t.Fatal("the rest of the policy did not come through")
	}

	// A policy that granted nothing keeps an absent list rather than an empty
	// one. Diff compares the fields whole, and an empty list is not the absent
	// one: an update of a package that grants nothing would report a change it
	// did not make.
	untouched := Override{SocketWayland: true}
	if got := untouched.WithMigratedFilesystem(); !reflect.DeepEqual(got, untouched) {
		t.Fatalf("a policy with no legacy field was rewritten: %+v", got)
	}
}
