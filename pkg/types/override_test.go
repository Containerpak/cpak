package types

import (
	"reflect"
	"testing"
)

func TestOverrideDiffReportsChangedPermissions(t *testing.T) {
	before := NewOverride()
	after := before
	after.Network = false
	after.FsExtra = []string{"/run/media"}

	changes := before.Diff(after)
	expected := []string{"fsExtra", "network"}
	if !reflect.DeepEqual(changes, expected) {
		t.Fatalf("expected %v, got %v", expected, changes)
	}
}
