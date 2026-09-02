/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

func HomeFilesystemSubpath(path string) (string, bool) {
	return homeFilesystemSubpath(path)
}

func XDGUserDirectory(scope string) (key, fallback string, ok bool) {
	key, ok = xdgDirectoryKeys[scope]
	if !ok {
		return "", "", false
	}
	return key, xdgDirectoryDefaults[key], true
}
