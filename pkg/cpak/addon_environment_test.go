/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestAddonExportsComposeRuntimeEnvironment(t *testing.T) {
	provider := types.Application{ParsedAddonProvider: &types.AddonProvider{
		ID:   "jdk25",
		Slot: "sdk.java",
		Mode: types.AddonSlotExclusive,
		Exports: types.AddonExports{
			Path:            []string{"/opt/jdk/bin"},
			LibraryPath:     []string{"/opt/jdk/lib"},
			IncludePath:     []string{"/opt/jdk/include"},
			PkgConfigPath:   []string{"/opt/jdk/lib/pkgconfig"},
			CMakePrefixPath: []string{"/opt/jdk"},
			Environment:     []string{"JAVA_HOME=/opt/jdk"},
		},
	}}
	environment := applyAddonEnvironment([]string{"PATH=/usr/bin", "LD_LIBRARY_PATH=/usr/lib"}, []types.Application{provider})
	checks := map[string]string{
		"PATH":              "/opt/jdk/bin:/usr/bin",
		"LD_LIBRARY_PATH":   "/opt/jdk/lib:/usr/lib",
		"LIBRARY_PATH":      "/opt/jdk/lib",
		"CPATH":             "/opt/jdk/include",
		"PKG_CONFIG_PATH":   "/opt/jdk/lib/pkgconfig",
		"CMAKE_PREFIX_PATH": "/opt/jdk",
		"JAVA_HOME":         "/opt/jdk",
	}
	for name, want := range checks {
		if got := environmentValue(environment, name); got != want {
			t.Fatalf("%s: got %q, want %q", name, got, want)
		}
	}
}
