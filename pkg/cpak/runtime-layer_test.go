/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"strings"
	"testing"

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
