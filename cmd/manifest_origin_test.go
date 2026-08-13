/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import "testing"

func TestNormalizeRepositoryOrigin(t *testing.T) {
	tests := map[string]string{
		"https://github.com/Containerpak/cpak.git": "github.com/containerpak/cpak",
		"git@github.com:Containerpak/cpak.git":     "github.com/containerpak/cpak",
		"github.com/Containerpak/cpak":             "github.com/containerpak/cpak",
	}
	for input, expected := range tests {
		actual, err := normalizeRepositoryOrigin(input)
		if err != nil {
			t.Fatalf("normalize %s: %v", input, err)
		}
		if actual != expected {
			t.Fatalf("normalize %s: got %s, want %s", input, actual, expected)
		}
	}
}

func TestNormalizeRepositoryOriginRejectsIncompleteValues(t *testing.T) {
	for _, input := range []string{"", "github.com/owner", "git@github.com:owner"} {
		if _, err := normalizeRepositoryOrigin(input); err == nil {
			t.Fatalf("accepted invalid origin %q", input)
		}
	}
}
