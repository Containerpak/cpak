/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

var builtAdapters = map[Backend]bool{}

func registerBuiltAdapter(backends ...Backend) {
	for _, backend := range backends {
		builtAdapters[backend] = true
	}
}

func adapterBuilt(backend Backend) bool {
	return builtAdapters[backend]
}
