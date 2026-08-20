/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/storaged"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestRuntimeLayerDigest(t *testing.T) {
	sources := []types.RuntimeSource{{
		Name:      "demo.deb",
		URL:       "https://example.com/demo.deb",
		SHA256:    strings.Repeat("a", 64),
		Size:      42,
		Installer: "dpkg",
	}}
	first := RuntimeLayerDigest([]string{"base", "top"}, sources)
	second := RuntimeLayerDigest([]string{"base", "top"}, sources)
	if first != second {
		t.Fatalf("digest changed for identical inputs: %s != %s", first, second)
	}
	if first == RuntimeLayerDigest([]string{"top", "base"}, sources) {
		t.Fatal("digest did not account for layer order")
	}
	changed := append([]types.RuntimeSource{}, sources...)
	changed[0].SHA256 = strings.Repeat("b", 64)
	if first == RuntimeLayerDigest([]string{"base", "top"}, changed) {
		t.Fatal("digest did not account for the artifact checksum")
	}
}

func TestRuntimeLayerUsesConfiguredStorage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CPAK_INSTALLATION_PATH", filepath.Join(root, "cpak"))
	t.Setenv("CPAK_STORAGE_BACKEND", "native")
	cp, err := NewCpak()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := storaged.NewFVS(cp.fvsRoot(), cp.storageDriverRoot("fvs"))
	if err != nil {
		t.Fatal(err)
	}
	cp.storageDriver = handler
	seedFVSLayerFile(t, &cp, "base", "usr/share/base", []byte("base"))

	payload := []byte("runtime")
	digest := sha256.Sum256(payload)
	cache := cp.GetInCacheDir("runtimes", hex.EncodeToString(digest[:]))
	if err = os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cache, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	stop := errors.New("storage preparation reached")
	called := false
	cp.SetStoragePreparationHandler(func(func() error) error {
		called = true
		return stop
	})
	_, err = cp.BuildRuntimeLayers([]string{"base"}, []types.RuntimeSource{{
		Name:      "runtime.tar",
		URL:       "https://example.com/runtime.tar",
		SHA256:    hex.EncodeToString(digest[:]),
		Size:      int64(len(payload)),
		Installer: "tar",
	}})
	if !errors.Is(err, stop) {
		t.Fatalf("runtime layer did not use the configured storage path: %v", err)
	}
	if !called {
		t.Fatal("runtime layer skipped configured storage preparation")
	}
}
