//go:build !cpak_ui_builtin && !cpak_ui_adwaita && !cpak_ui_gtk && !cpak_ui_kde && !cpak_ui_qt

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

func init() {
	registerBuiltAdapter(BackendAdwaita, BackendGTK, BackendKDE, BackendQt)
}
