/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import "github.com/mirkobrombin/cpak/pkg/cpak"

func resolveApplicationOrigin(cp cpak.Cpak, value string) (string, error) {
	return cp.ResolveInstalledOrigin(value)
}

func resolveRunOrigin(cp cpak.Cpak, value string) (string, error) {
	return cp.ResolveOrigin(value)
}
