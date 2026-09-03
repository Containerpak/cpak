/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import "testing"

func TestAppArmorRelaunchSkipsCommandsThatNeedNoNamespaces(t *testing.T) {
	for _, arguments := range [][]string{
		{"cpak"},
		{"cpak", "--help"},
		{"cpak", "--version"},
		{"cpak", "self-update"},
		{"cpak", "system", "setup"},
		{"cpak", "system", "remove"},
		{"cpak", "auth", "list"},
		{"cpak", "list"},
		{"cpak", "init"},
	} {
		if needsAppArmorRuntime(arguments) {
			t.Fatalf("non-runtime command would relaunch: %v", arguments)
		}
	}
}

func TestAppArmorRelaunchCoversRuntimeCommands(t *testing.T) {
	for _, command := range []string{"doctor", "install", "update", "run", "shell", "storage", "service", "environment"} {
		if !needsAppArmorRuntime([]string{"cpak", command}) {
			t.Fatalf("runtime command would stay in a user-owned executable: %s", command)
		}
	}
}
