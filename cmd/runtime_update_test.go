/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdatedRuntimeRefreshesOnlyTheSystemCopyThatNeedsIt(t *testing.T) {
	for _, name := range []string{"absent", "current", "older", "required", "refused", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			argsFile := filepath.Join(t.TempDir(), "arguments")
			t.Setenv("CPAK_NESTED_TEST_ARGS_FILE", argsFile)
			t.Setenv("CPAK_NESTED_TEST_STATUS", "0")
			old := filepath.Join(t.TempDir(), "cpak")
			if err := os.WriteFile(old, []byte("previous release"), 0755); err != nil {
				t.Fatal(err)
			}
			savedInstalled, savedRestricted := installedRuntimeExecutable, runtimeNamespacesRestricted
			t.Cleanup(func() {
				installedRuntimeExecutable = savedInstalled
				runtimeNamespacesRestricted = savedRestricted
			})
			installedRuntimeExecutable = func() (string, bool) {
				if name == "current" {
					return executable, true
				}
				return old, name != "absent" && name != "required"
			}
			runtimeNamespacesRestricted = func() bool { return name == "required" }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			if name == "refused" {
				t.Setenv("CPAK_NESTED_TEST_STATUS", "23")
			}
			err = prepareUpdatedRuntime(ctx, executable)
			if name == "refused" || name == "cancelled" {
				if err == nil {
					t.Fatal("runtime preparation failed but the upgrade was allowed to continue")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			args, readErr := os.ReadFile(argsFile)
			if name == "absent" || name == "current" || name == "cancelled" {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("setup was invoked without needing or being allowed to refresh it: %q, %v", args, readErr)
				}
			} else if readErr != nil || string(args) != "system\nsetup\n" {
				t.Fatalf("the replacement executable did not receive the setup command: %q, %v", args, readErr)
			}
		})
	}
}
