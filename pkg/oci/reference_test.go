/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package oci

import (
	"strings"
	"testing"
)

func TestParseReferenceNormalizesNames(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	tests := map[string]string{
		"ubuntu":                            "index.docker.io/library/ubuntu:latest",
		"docker.io/library/ubuntu":          "index.docker.io/library/ubuntu:latest",
		"ghcr.io/example/app":               "ghcr.io/example/app:latest",
		"localhost:5000/example/app:test":   "localhost:5000/example/app:test",
		"ghcr.io/example/app:old@" + digest: "ghcr.io/example/app@" + digest,
	}
	for input, expected := range tests {
		ref, err := ParseReference(input)
		if err != nil {
			t.Fatalf("parse %s: %v", input, err)
		}
		if ref.Name() != expected {
			t.Fatalf("parse %s: got %s, want %s", input, ref.Name(), expected)
		}
	}
}

func TestParseReferenceRejectsInvalidNames(t *testing.T) {
	for _, input := range []string{"", "ghcr.io/Upper/app", "ghcr.io/app:bad tag", "ghcr.io/app@sha256:abc", "https://ghcr.io/app"} {
		if _, err := ParseReference(input); err == nil {
			t.Fatalf("accepted invalid reference %q", input)
		}
	}
}
