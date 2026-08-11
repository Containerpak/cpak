/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSystemBrokerTokenRequiresPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("01234567890123456789012345678901"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSystemBrokerToken(path); err != nil {
		t.Fatalf("private token: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSystemBrokerToken(path); err == nil {
		t.Fatal("public token file was accepted")
	}
}
