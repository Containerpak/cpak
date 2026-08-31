/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"reflect"
	"testing"
)

func TestNixStoreRootsIncludeTheExecutableAndDynamicRuntime(t *testing.T) {
	paths := []string{
		"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-glibc-2.40/lib/ld-linux-x86-64.so.2",
		"/nix/store/cccccccccccccccccccccccccccccccc-zlib-1.3/lib",
		"/usr/lib/libc.so.6",
		"$ORIGIN/../lib",
	}
	want := []string{
		"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cpak-2.10.12",
		"/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-glibc-2.40",
		"/nix/store/cccccccccccccccccccccccccccccccc-zlib-1.3",
	}
	got := nixStoreRoots("/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-cpak-2.10.12/bin/cpak", paths)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Nix runtime roots: got %v, want %v", got, want)
	}
}

func TestExecutableRuntimePathsReadsTheRunningBinary(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executableRuntimePaths(binary); err != nil {
		t.Fatal(err)
	}
}
