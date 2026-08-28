/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import "testing"

func TestNormalizeRepositoryOrigin(t *testing.T) {
	tests := map[string]string{
		"https://GITHUB.COM/containerpak/cpak.git": "github.com/containerpak/cpak",
		"git@GITHUB.COM:containerpak/cpak.git":     "github.com/containerpak/cpak",
		"GITHUB.COM/containerpak/cpak":             "github.com/containerpak/cpak",
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
	for _, input := range []string{"", "github.com/owner", "git@github.com:owner", "github.com/Containerpak/cpak"} {
		if _, err := normalizeRepositoryOrigin(input); err == nil {
			t.Fatalf("accepted invalid origin %q", input)
		}
	}
}
