/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstRegularFileUsesFirstAvailablePath(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	if err := os.WriteFile(second, []byte("NAME=Host\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := firstRegularFile(first, second); got != second {
		t.Fatalf("host OS release: got %q, want %q", got, second)
	}
}

func TestFirstRegularFileResolvesSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "os-release")
	link := filepath.Join(directory, "host-os-release")
	if err := os.WriteFile(target, []byte("NAME=Host\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if got := firstRegularFile(link); got != target {
		t.Fatalf("host OS release: got %q, want %q", got, target)
	}
}

func TestFirstRegularFileRejectsDirectories(t *testing.T) {
	if got := firstRegularFile(t.TempDir()); got != "" {
		t.Fatalf("host OS release accepted directory %q", got)
	}
}
