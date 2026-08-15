/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

var brandIconPNG []byte

// SetBrandIcon configures the icon used by built-in cpak dialogs.
func SetBrandIcon(icon []byte) {
	brandIconPNG = append(brandIconPNG[:0], icon...)
}
