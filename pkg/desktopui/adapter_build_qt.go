//go:build cpak_ui_qt

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

func init() {
	buildHasQt = true
	registerBuiltAdapter(BackendQt)
}
