package types

import (
	"reflect"
	"testing"
)

func TestOverrideDiffReportsChangedPermissions(t *testing.T) {
	before := NewOverride()
	after := before
	after.Network = false
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
