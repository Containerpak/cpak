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

func TestReadEnvironmentPolicyEnforcesSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), environmentPolicySizeLimit+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvironmentPolicy(path); err == nil {
		t.Fatal("read an environment policy larger than the configured limit")
	}
}

func TestReadEnvironmentPolicyRejectsMultipleValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte("{}\n{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvironmentPolicy(path); err == nil {
		t.Fatal("read an environment policy containing multiple JSON values")
	}
}
