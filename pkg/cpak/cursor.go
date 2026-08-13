/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var cursorThemePattern = regexp.MustCompile(`^[A-Za-z0-9_.+-]{1,128}$`)

func inheritHostCursor(environment []string) []string {
	theme := environmentValue(environment, "XCURSOR_THEME")
	size := environmentValue(environment, "XCURSOR_SIZE")
	if theme == "" {
		theme = os.Getenv("XCURSOR_THEME")
	}
	if size == "" {
		size = os.Getenv("XCURSOR_SIZE")
	}
	if theme == "" {
		theme = readCursorSetting("cursor-theme")
	}
	if size == "" {
		size = readCursorSetting("cursor-size")
	}
	if cursorThemePattern.MatchString(theme) && environmentValue(environment, "XCURSOR_THEME") == "" {
		environment = append(environment, "XCURSOR_THEME="+theme)
	}
	if validCursorSize(size) && environmentValue(environment, "XCURSOR_SIZE") == "" {
		environment = append(environment, "XCURSOR_SIZE="+size)
	}
	return environment
}

func readCursorSetting(key string) string {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(ctx, "gsettings", "get", "org.gnome.desktop.interface", key).Output()
	if err != nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(string(output)), "'")
}

func validCursorSize(value string) bool {
	size, err := strconv.Atoi(value)
	return err == nil && size > 0 && size <= 1024
}
