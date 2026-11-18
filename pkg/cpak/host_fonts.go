/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const hostFontConfigTarget = "/run/cpak/host-fonts.conf"
const hostFontCacheTarget = "/run/cpak/fontconfig-cache"

const nativeHostFontsConfig = `<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
	<cachedir>/run/cpak/fontconfig-cache</cachedir>
	<include ignore_missing="yes">/etc/fonts/fonts.conf</include>
	<dir>/run/host/fonts</dir>
	<dir>/run/host/local-fonts</dir>
	<dir>/run/host/user-fonts</dir>
	<dir>/run/host/legacy-user-fonts</dir>
</fontconfig>
`

func (c *Cpak) nativeFontArguments(container types.Container, override types.Override) ([]string, error) {
	if !override.HostFonts {
		return nil, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	directories := [][2]string{
		{"/usr/share/fonts", "/run/host/fonts"},
		{"/usr/local/share/fonts", "/run/host/local-fonts"},
		{filepath.Join(home, ".local", "share", "fonts"), "/run/host/user-fonts"},
		{filepath.Join(home, ".fonts"), "/run/host/legacy-user-fonts"},
	}
	arguments := []string{}
	for _, directory := range directories {
		info, err := os.Stat(directory[0])
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			continue
		}
		resolved, err := filepath.EvalSymlinks(directory[0])
		if err != nil {
			return nil, err
		}
		for _, state := range c.cpakStateDirectories() {
			if canonical, err := filepath.EvalSymlinks(state); err == nil {
				state = canonical
			}
			if types.PathIsCpakState(resolved, []string{state}) || types.PathIsCpakState(state, []string{resolved}) {
				return nil, fmt.Errorf("host font directory overlaps cpak state: %s", directory[0])
			}
		}
		arguments = append(arguments, "--extra-links", resolved+":"+directory[1])
	}
	config := filepath.Join(container.StatePath, "host-fonts.conf")
	if err := os.WriteFile(config, []byte(nativeHostFontsConfig), 0o600); err != nil {
		return nil, err
	}
	cache := filepath.Join(container.StatePath, "fontconfig-cache")
	if err := os.Mkdir(cache, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(cache)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private font cache is not a directory: %s", cache)
	}
	return append(arguments, "--extra-links", config+":"+hostFontConfigTarget,
		"--writable-links", cache+":"+hostFontCacheTarget), nil
}
