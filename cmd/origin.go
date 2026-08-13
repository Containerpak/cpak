/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import "github.com/mirkobrombin/cpak/pkg/cpak"

func resolveApplicationOrigin(cp cpak.Cpak, value string) (string, error) {
	return cp.ResolveInstalledOrigin(value)
}
