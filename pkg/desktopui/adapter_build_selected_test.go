//go:build cpak_ui_adwaita || cpak_ui_gtk || cpak_ui_kde || cpak_ui_qt

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import "testing"

func TestSelectedAdapterBuildDoesNotIncludeUnselectedAdapters(t *testing.T) {
	selected := map[Backend]bool{
		BackendAdwaita: buildHasAdwaita,
		BackendGTK:     buildHasGTK,
		BackendKDE:     buildHasKDE,
		BackendQt:      buildHasQt,
	}
	for backend, expected := range selected {
		if adapterBuilt(backend) != expected {
			t.Fatalf("adapter %s built=%t, expected %t", backend, adapterBuilt(backend), expected)
		}
	}
}
