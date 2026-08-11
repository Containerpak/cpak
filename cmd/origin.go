/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import "github.com/mirkobrombin/cpak/pkg/cpak"

func resolveApplicationOrigin(cp cpak.Cpak, value string) (string, error) {
	return cp.ResolveInstalledOrigin(value)
}
