/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLegacyRuntimeTools(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"nsenter", "rootlessctl", "rootlesskit", "rootlesskit-docker-proxy", "keep"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	cleanupLegacyRuntimeTools(directory)

	if _, err := os.Stat(filepath.Join(directory, "keep")); err != nil {
		t.Fatalf("unrelated tool was removed: %v", err)
	}
	for _, name := range []string{"nsenter", "rootlessctl", "rootlesskit", "rootlesskit-docker-proxy"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy tool %s remains", name)
		}
	}
}

func TestDoctorRequiredChecksHaveDetails(t *testing.T) {
	report := Doctor()
	for _, check := range report.Checks {
		if check.Name == "" || check.Detail == "" {
			t.Fatalf("incomplete check: %#v", check)
		}
	}
}
