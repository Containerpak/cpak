//go:build cpak_ui_gtk

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

func init() {
	buildHasGTK = true
	registerBuiltAdapter(BackendGTK)
}
