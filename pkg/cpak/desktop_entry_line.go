/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import "strings"

// desktopEntryKey answers which key a line of a desktop entry sets, and what it
// sets it to, the way the parser every launcher actually uses reads it.
//
// It exists because a byte prefix is not that parser. GLib skips whitespace
// before the key and around the equals sign, so " Exec=", a tab before Exec,
// and "Exec =" all set Exec, and a rewrite testing HasPrefix(line, "Exec=")
// leaves every one of them exactly as the publisher wrote it. The whole of a
// desktop entry comes from the publisher, replacing its Exec line is the only
// thing keeping a menu launch inside the sandbox, and a launcher that reads a
// line cpak did not read is how that is lost. Duplicate keys make it worse:
// GLib keeps the last, so an entry can carry a rewritten line for cpak to find
// and an untouched one for the launcher to run.
//
// Locale suffixes are deliberately not folded away. "Exec[it]" is a different
// key here because it is a different key to GDesktopAppInfo, which reads Exec
// without a locale and would never run it.
func desktopEntryKey(line string) (key string, value string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	name, rest, found := strings.Cut(trimmed, "=")
	if !found {
		return "", "", false
	}
	name = strings.TrimRight(name, " \t")
	if name == "" {
		return "", "", false
	}
	return name, strings.TrimLeft(rest, " \t"), true
}

// desktopEntryGroup answers the group a line opens, for a reader that has to
// know which section it is in.
func desktopEntryGroup(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	return trimmed, true
}
