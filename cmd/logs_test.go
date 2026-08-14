/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPrintLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "application.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := printLogTail(path, 2, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "two\nthree\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
