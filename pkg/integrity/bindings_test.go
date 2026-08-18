/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package integrity

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	firstLayer  = "1111111111111111111111111111111111111111111111111111111111111111"
	secondLayer = "2222222222222222222222222222222222222222222222222222222222222222"
)

func newBindings(t *testing.T) *DirectoryBindings {
	t.Helper()

	bindings, err := NewDirectoryBindings(filepath.Join(t.TempDir(), "bindings"))
	if err != nil {
		t.Fatalf("the bindings directory could not be prepared: %v", err)
	}
	return bindings
}

func TestBindingRoundTrip(t *testing.T) {
	bindings := newBindings(t)
	want := LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "root"}
	if err := bindings.Bind(want); err != nil {
		t.Fatalf("a first binding was refused: %v", err)
	}
	got, found, err := bindings.Lookup(firstLayer)
	if err != nil || !found {
		t.Fatalf("a recorded binding was not found: found=%v err=%v", found, err)
	}
	if got != want {
		t.Fatalf("got %+v, want the binding that was recorded %+v", got, want)
	}
}

// Both spellings name one layer, so they must reach one record.
func TestBindingAcceptsThePrefixedSpelling(t *testing.T) {
	bindings := newBindings(t)
	if err := bindings.Bind(LayerBinding{OCIDigest: "sha256:" + firstLayer, StateID: "state", StateRoot: "root"}); err != nil {
		t.Fatalf("a prefixed digest was refused: %v", err)
	}
	got, found, err := bindings.Lookup(firstLayer)
	if err != nil || !found {
		t.Fatalf("the bare spelling did not reach the record: found=%v err=%v", found, err)
	}
	if got.OCIDigest != firstLayer {
		t.Fatalf("got %q, want the digest stored in its canonical form", got.OCIDigest)
	}
}

func TestBindingRepeatedIsAccepted(t *testing.T) {
	bindings := newBindings(t)
	binding := LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "root"}
	if err := bindings.Bind(binding); err != nil {
		t.Fatalf("a first binding was refused: %v", err)
	}
	if err := bindings.Bind(binding); err != nil {
		t.Fatalf("recording the same binding twice was refused: %v", err)
	}
}

func TestBindingConflictIsRefusedAndChangesNothing(t *testing.T) {
	bindings := newBindings(t)
	first := LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "root"}
	if err := bindings.Bind(first); err != nil {
		t.Fatalf("a first binding was refused: %v", err)
	}
	err := bindings.Bind(LayerBinding{OCIDigest: firstLayer, StateID: "other", StateRoot: "root"})
	if !errors.Is(err, errBindingConflict) {
		t.Fatalf("got %v, want a layer bound to a second state to be refused", err)
	}
	got, found, err := bindings.Lookup(firstLayer)
	if err != nil || !found || got != first {
		t.Fatalf("the refused binding changed the record: %+v found=%v err=%v", got, found, err)
	}
	assertOnlyRecords(t, bindings, firstLayer)
}

// A state root is part of the answer, so replacing only that is a conflict too.
func TestBindingConflictCoversTheStateRoot(t *testing.T) {
	bindings := newBindings(t)
	if err := bindings.Bind(LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "root"}); err != nil {
		t.Fatalf("a first binding was refused: %v", err)
	}
	err := bindings.Bind(LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "other"})
	if !errors.Is(err, errBindingConflict) {
		t.Fatalf("got %v, want a second state root for one layer to be refused", err)
	}
}

func TestBindingRefusesAnUnboundLayer(t *testing.T) {
	bindings := newBindings(t)
	for _, binding := range []LayerBinding{
		{OCIDigest: firstLayer, StateRoot: "root"},
		{OCIDigest: firstLayer, StateID: "state"},
	} {
		if err := bindings.Bind(binding); !errors.Is(err, errUnboundLayer) {
			t.Fatalf("got %v, want a binding without a state to be refused", err)
		}
	}
	assertOnlyRecords(t, bindings)
}

func TestLookupOfAnUnknownLayerIsNotAnError(t *testing.T) {
	bindings := newBindings(t)
	got, found, err := bindings.Lookup(secondLayer)
	if err != nil || found || got != (LayerBinding{}) {
		t.Fatalf("an unknown layer answered: %+v found=%v err=%v", got, found, err)
	}
}

// Nothing but sixty-four hexadecimal characters may reach the path, so a
// reference that could name another directory is refused before it is used.
func TestBindingRefusesADigestThatIsNotSHA256(t *testing.T) {
	bindings := newBindings(t)
	for _, reference := range []string{
		"",
		"sha256:",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64),
		"sha512:" + strings.Repeat("a", 64),
		"../" + strings.Repeat("a", 61),
		strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31),
		"..",
	} {
		if err := bindings.Bind(LayerBinding{OCIDigest: reference, StateID: "state", StateRoot: "root"}); !errors.Is(err, errInvalidLayerDigest) {
			t.Fatalf("binding %q: got %v, want an implausible digest to be refused", reference, err)
		}
		if _, _, err := bindings.Lookup(reference); !errors.Is(err, errInvalidLayerDigest) {
			t.Fatalf("lookup %q: got %v, want an implausible digest to be refused", reference, err)
		}
	}
	assertOnlyRecords(t, bindings)
}

func TestBindingsDirectoryMustBeAbsolute(t *testing.T) {
	if _, err := NewDirectoryBindings("bindings"); err == nil {
		t.Fatal("a relative bindings directory was accepted")
	}
}

// A record moved onto another layer's name still says which layer it describes.
func TestLookupRefusesARecordFiledUnderAnotherLayer(t *testing.T) {
	bindings := newBindings(t)
	if err := bindings.Bind(LayerBinding{OCIDigest: firstLayer, StateID: "state", StateRoot: "root"}); err != nil {
		t.Fatalf("a first binding was refused: %v", err)
	}
	moved := filepath.Join(bindings.directory, secondLayer+".json")
	if err := os.Rename(bindings.path(firstLayer), moved); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindings.Lookup(secondLayer); err == nil {
		t.Fatal("a record answered for a layer it does not name")
	}
}

func TestLookupRefusesARecordWithTrailingContent(t *testing.T) {
	bindings := newBindings(t)
	record := `{"oci_digest":"` + firstLayer + `","state_id":"state","state_root":"root"}`
	if err := os.WriteFile(bindings.path(firstLayer), []byte(record+record), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindings.Lookup(firstLayer); err == nil {
		t.Fatal("a record carrying a second value was accepted")
	}
}

func TestLookupRefusesARecordWithUnknownFields(t *testing.T) {
	bindings := newBindings(t)
	record := `{"oci_digest":"` + firstLayer + `","state_id":"state","state_root":"root","trusted":true}`
	if err := os.WriteFile(bindings.path(firstLayer), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindings.Lookup(firstLayer); err == nil {
		t.Fatal("a record carrying an unknown field was accepted")
	}
}

func TestLookupRefusesAnOversizedRecord(t *testing.T) {
	bindings := newBindings(t)
	padding := strings.Repeat("p", bindingLimit)
	record := `{"oci_digest":"` + firstLayer + `","state_id":"` + padding + `","state_root":"root"}`
	if err := os.WriteFile(bindings.path(firstLayer), []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bindings.Lookup(firstLayer); err == nil {
		t.Fatal("a record larger than any this store writes was accepted")
	}
}

// assertOnlyRecords fails unless the directory holds exactly the named records
// and nothing else, so a refused write cannot leave a staged file behind.
func assertOnlyRecords(t *testing.T, bindings *DirectoryBindings, digests ...string) {
	t.Helper()

	entries, err := os.ReadDir(bindings.directory)
	if err != nil {
		t.Fatal(err)
	}
	wanted := make(map[string]bool, len(digests))
	for _, digest := range digests {
		wanted[digest+".json"] = true
	}
	if len(entries) != len(wanted) {
		t.Fatalf("the directory holds %d files, want %d", len(entries), len(wanted))
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			t.Fatalf("the directory holds an unexpected file: %s", entry.Name())
		}
	}
}
