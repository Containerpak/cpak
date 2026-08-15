//go:build cpak_ui_builtin

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"strings"
	"testing"
)

func TestBuiltinBuildExcludesDesktopAdapters(t *testing.T) {
	for _, backend := range []Backend{BackendAdwaita, BackendGTK, BackendKDE, BackendQt} {
		if adapterBuilt(backend) {
			t.Fatalf("adapter %s is part of a builtin-only build", backend)
		}
	}
}

func TestBuiltinBuildRejectsExternalAdapters(t *testing.T) {
	_, err := adapterExecutable(BackendGTK)
	if err == nil || !strings.Contains(err.Error(), "not part of this build") {
		t.Fatalf("unexpected error: %v", err)
	}
}
