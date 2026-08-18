/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/oci"
)

const boundLayer = "3333333333333333333333333333333333333333333333333333333333333333"

func layerState(t *testing.T, cp *Cpak, digest string) string {
	t.Helper()

	states, err := fvsrepo.States(cp.fvsLayerPath(digest))
	if err != nil || len(states) == 0 {
		t.Fatalf("the seeded layer holds no state: %v", err)
	}
	return states[0].ID
}

func TestRecordLayerBindingNamesThePublishedState(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, boundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(boundLayer); err != nil {
		t.Fatalf("the binding was refused: %v", err)
	}

	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding, found, err := bindings.Lookup(boundLayer)
	if err != nil || !found {
		t.Fatalf("the recorded binding was not found: found=%v err=%v", found, err)
	}
	state := layerState(t, cp, boundLayer)
	if binding.StateID != state {
		t.Fatalf("got state %q, want the state the layer repository holds %q", binding.StateID, state)
	}
	root, err := layerStateRoot(cp.fvsLayerPath(boundLayer), state)
	if err != nil {
		t.Fatal(err)
	}
	if binding.StateRoot != root {
		t.Fatalf("got root %q, want the root of the published state %q", binding.StateRoot, root)
	}
}

func TestRecordLayerBindingIsRepeatable(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, boundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(boundLayer); err != nil {
		t.Fatalf("the binding was refused: %v", err)
	}
	if err := cp.recordLayerBinding(boundLayer); err != nil {
		t.Fatalf("recording the same layer twice was refused: %v", err)
	}
}

// A layer that is bound cannot quietly become another state, whatever put that
// state in its place.
func TestRecordLayerBindingRefusesAReplacedState(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, boundLayer, "usr/share/value", []byte("payload"))
	if err := cp.recordLayerBinding(boundLayer); err != nil {
		t.Fatalf("the binding was refused: %v", err)
	}
	writer, err := fvsrepo.BeginSnapshot(cp.fvsLayerPath(boundLayer), fvsrepo.SnapshotOptions{ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Add(fvsrepo.Entry{Path: "usr/share/value", Kind: fvsrepo.EntryFile, Mode: 0o644, Size: 7}, strings.NewReader("swapped")); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = cp.recordLayerBinding(boundLayer); err == nil {
		t.Fatal("a layer was rebound to a state it was never proven to hold")
	}
}

func TestRecordLayerBindingRefusesALayerWithoutAState(t *testing.T) {
	cp := newTestCpak(t)
	if err := cp.recordLayerBinding(boundLayer); err == nil {
		t.Fatal("a layer that was never stored was bound")
	}
}

// The root answers for what the store holds, so a different tree is a different
// root and the same tree is the same root.
func TestLayerStateRootFollowsTheStoredTree(t *testing.T) {
	cp := newTestCpak(t)
	seedFVSLayerFile(t, cp, boundLayer, "usr/share/value", []byte("payload"))
	repository := cp.fvsLayerPath(boundLayer)
	state := layerState(t, cp, boundLayer)
	first, err := layerStateRoot(repository, state)
	if err != nil {
		t.Fatal(err)
	}
	second, err := layerStateRoot(repository, state)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the root of one state was read twice as %q and %q", first, second)
	}

	other := newTestCpak(t)
	seedFVSLayerFile(t, other, boundLayer, "usr/share/value", []byte("different"))
	changed, err := layerStateRoot(other.fvsLayerPath(boundLayer), layerState(t, other, boundLayer))
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("two different trees produced the same root")
	}
}

// A binding is only worth as much as the check that preceded it, so a layer
// rebuilt from ranges, which is never hashed back to the digest it would be
// stored under, is refused before it reaches the store.
// A chunked pull rebuilds the layer from ranges and never reads the blob its
// digest names, so it cannot produce a binding for that digest. The pull is
// still allowed, because partial transfer is the reason it exists, and the gap
// shows up later as a layer the gate cannot describe.
func TestChunkedLayerCarriesNoBinding(t *testing.T) {
	cp := newTestCpak(t)
	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	if _, found, lookupErr := bindings.Lookup(boundLayer); lookupErr != nil || found {
		t.Fatalf("a layer that was never pulled is already bound: found=%v err=%v", found, lookupErr)
	}
}

func TestDownloadLayerBindsTheLayerItVerified(t *testing.T) {
	content := testLayer(t, mediaOCILayerGzip, []testLayerEntry{
		{name: "usr/share/value", typeflag: tar.TypeReg, mode: 0644, content: []byte("downloaded")},
	})
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(content)
	}))
	defer server.Close()

	cp := newTestCpak(t)
	ref, err := oci.ParseReference(strings.TrimPrefix(server.URL, "http://") + "/example/app")
	if err != nil {
		t.Fatal(err)
	}
	descriptor := oci.Descriptor{
		Digest:    "sha256:" + digest,
		Size:      int64(len(content)),
		MediaType: mediaOCILayerGzip,
	}
	if err = cp.downloadLayer(&oci.Client{HTTP: server.Client()}, ref, descriptor, digest); err != nil {
		t.Fatal(err)
	}

	bindings, err := cp.layerBindings()
	if err != nil {
		t.Fatal(err)
	}
	binding, found, err := bindings.Lookup(digest)
	if err != nil || !found {
		t.Fatalf("a verified download left no binding: found=%v err=%v", found, err)
	}
	if binding.StateID != layerState(t, cp, digest) {
		t.Fatalf("got state %q, want the state the download committed", binding.StateID)
	}
}
