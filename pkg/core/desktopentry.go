/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"crypto/sha256"
	"fmt"
	"path"
	"strconv"
	"strings"
)

const (
	desktopFileSpanFlag = "--desktop-file-span"
	legacyGrantStart    = "@@cpak:file-grant:start@@"
	legacyGrantEnd      = "@@cpak:file-grant:end@@"
)

var desktopFilePlaceholders = map[string]bool{"%f": true, "%F": true, "%u": true, "%U": true}

type desktopFileSpan struct {
	Before int
	After  int
}

func (s desktopFileSpan) String() string {
	return strconv.Itoa(s.Before) + "," + strconv.Itoa(s.After)
}

// DesktopExport is everything an exported entry needs that the entry itself
// does not carry: the program that will run it, the application it belongs to,
// and the icon that was extracted for it.
type DesktopExport struct {
	// Launcher is the absolute path of the cpak binary the menu will run.
	Launcher string

	// Origin is the application origin the launcher is asked to run.
	Origin string

	// CpakID identifies the installation, and names the exported files.
	CpakID string

	// Icon is the exported icon path. An empty icon leaves the entry's own
	// Icon line alone.
	Icon string
}

// DesktopEntryKey answers which key a line of a desktop entry sets, and what it
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
func DesktopEntryKey(line string) (key string, value string, ok bool) {
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

// DesktopEntryGroup answers the group a line opens, for a reader that has to
// know which section it is in.
func DesktopEntryGroup(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	return trimmed, true
}

// DesktopEntryValue answers a key from the [Desktop Entry] group of an entry,
// and empty when the group does not set it.
func DesktopEntryValue(data []byte, key string) string {
	inDesktopEntry := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inDesktopEntry = line == "[Desktop Entry]"
			continue
		}
		if inDesktopEntry && strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+"="))
		}
	}
	return ""
}

// SetDesktopEntryValue sets a key in the [Desktop Entry] group, adding it at
// the end of the group when it is not there yet. An entry with no such group is
// returned unchanged, because inventing one would publish a launcher the
// publisher never wrote.
func SetDesktopEntryValue(content, key, value string) string {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	inDesktopEntry := false
	insertAt := -1
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			if inDesktopEntry {
				insertAt = i
				break
			}
			inDesktopEntry = line == "[Desktop Entry]"
			continue
		}
		if !inDesktopEntry {
			continue
		}
		insertAt = i + 1
		if strings.HasPrefix(line, key+"=") {
			lines[i] = key + "=" + value
			return strings.Join(lines, "\n")
		}
	}
	if insertAt < 0 {
		return content
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:])
	lines[insertAt] = key + "=" + value
	return strings.Join(lines, "\n")
}

// RewriteDesktopEntry answers the entry cpak exports for an installed
// application.
//
// Three keys decide where a menu click ends up. Exec becomes a cpak run of the
// published command, TryExec becomes cpak itself so a launcher testing for the
// program finds the one that exists, and Icon becomes the icon that was
// extracted from the image. Everything else the publisher wrote is left exactly
// as it was, including keys cpak has no opinion about.
func RewriteDesktopEntry(content string, export DesktopExport) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		key, value, ok := DesktopEntryKey(line)
		if !ok {
			continue
		}
		switch key {
		case "Exec":
			lines[index] = RewriteDesktopExec(export.Launcher, export.Origin, value)
		case "TryExec":
			lines[index] = "TryExec=" + export.Launcher
		case "Icon":
			if export.Icon != "" {
				lines[index] = "Icon=" + export.Icon
			}
		}
	}
	return strings.Join(lines, "\n")
}

// DesktopAliasEntry answers the copy of an exported entry that keeps the
// publisher's own file name, so a launcher that was told about the application
// by name still finds it. It is hidden from menus and carries the two keys that
// say whose it is, because a later export has to be able to tell its own alias
// apart from an unrelated entry it must not overwrite.
func DesktopAliasEntry(content string, export DesktopExport) string {
	content = SetDesktopEntryValue(content, "NoDisplay", "true")
	content = SetDesktopEntryValue(content, "X-cpak-Origin", export.Origin)
	return SetDesktopEntryValue(content, "X-cpak-ID", export.CpakID)
}

// DesktopExportID names the exported files of an installation. It is a digest
// rather than the identifier itself so the name is always a single path element
// of a known length, whatever the identifier turns out to contain.
func DesktopExportID(cpakID string) string {
	hash := sha256.Sum256([]byte(cpakID))
	return fmt.Sprintf("cpak-%x", hash)
}

// DesktopExportFileName answers the file name an entry is exported under.
func DesktopExportFileName(cpakID, entry string) string {
	return DesktopExportID(cpakID) + "-" + path.Base(entry)
}

// RewriteDesktopExec turns a published command into the cpak run that replaces
// it.
//
// Only the first word is the program, and finding where it ends is the whole
// job: a command may quote a path with spaces in it, and the arguments after it
// belong to the application rather than to cpak. The program is marked with an
// @ so the run resolves it as an exported binary. File arguments are described
// by counts carried in a cpak flag, never by markers mixed into publisher text.
func RewriteDesktopExec(launcher, origin, command string) string {
	command = strings.TrimSpace(command)
	end := len(command)
	quoted := false
	escaped := false
	for i := 0; i < len(command); i++ {
		if escaped {
			escaped = false
			continue
		}
		switch command[i] {
		case '\\':
			escaped = true
		case '"':
			quoted = !quoted
		case ' ', '\t':
			if !quoted {
				end = i
				i = len(command)
			}
		}
	}

	binary := command[:end]
	if strings.HasPrefix(binary, "\"") {
		binary = "\"@" + strings.TrimPrefix(binary, "\"")
	} else {
		binary = "@" + binary
	}
	rewritten := "Exec=" + DesktopExecArgument(launcher) + " run --desktop-launch " + origin
	arguments := strings.TrimSpace(command[end:])
	span, selects, err := countDesktopFileSpan(splitDesktopArguments(arguments))
	if err == nil && selects {
		rewritten += " " + desktopFileSpanFlag + " " + span.String()
	}
	rewritten += " " + binary
	if arguments != "" {
		rewritten += " -- " + arguments
	}
	return rewritten
}

// DesktopExecArgument quotes a value for an Exec line, which is read by a shell
// word splitter that is not a shell.
func DesktopExecArgument(value string) string {
	if !strings.ContainsAny(value, " \t\n\"`$\\") {
		return value
	}
	value = strings.NewReplacer(
		"\\", "\\\\",
		"\"", "\\\"",
		"`", "\\`",
		"$", "\\$",
	).Replace(value)
	return "\"" + value + "\""
}

func splitDesktopArguments(value string) []string {
	tokens := []string{}
	start := 0
	quoted := false
	escaped := false
	add := func(token string) {
		if token == "" {
			return
		}
		tokens = append(tokens, token)
	}
	for index := 0; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		switch value[index] {
		case '\\':
			escaped = true
		case '"':
			quoted = !quoted
		case ' ', '\t':
			if quoted {
				continue
			}
			add(value[start:index])
			start = index + 1
		}
	}
	add(value[start:])
	return tokens
}

func countDesktopFileSpan(arguments []string) (desktopFileSpan, bool, error) {
	placeholder := -1
	for index, argument := range arguments {
		if !desktopFilePlaceholders[strings.Trim(argument, `"`)] {
			continue
		}
		if placeholder >= 0 {
			return desktopFileSpan{}, false, fmt.Errorf("desktop entry names more than one file placeholder")
		}
		placeholder = index
	}
	if placeholder < 0 {
		return desktopFileSpan{}, false, nil
	}
	return desktopFileSpan{Before: placeholder, After: len(arguments) - placeholder - 1}, true, nil
}

// RepairDesktopLauncher updates an entry that was exported by an earlier cpak.
func RepairDesktopLauncher(content, launcher string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		switch {
		case line == "Exec=cpak":
			lines[i] = "Exec=" + DesktopExecArgument(launcher)
		case strings.HasPrefix(line, "Exec=cpak "):
			lines[i] = "Exec=" + DesktopExecArgument(launcher) + strings.TrimPrefix(line, "Exec=cpak")
		case line == "TryExec=cpak":
			lines[i] = "TryExec=" + launcher
		}
		prefix := "Exec=" + DesktopExecArgument(launcher) + " run "
		if strings.HasPrefix(lines[i], prefix) && !strings.HasPrefix(lines[i], prefix+"--desktop-launch ") {
			lines[i] = prefix + "--desktop-launch " + strings.TrimPrefix(lines[i], prefix)
		}
		if strings.HasPrefix(lines[i], prefix+"--desktop-launch ") {
			lines[i] = stripLegacyGrantMarkers(lines[i])
			lines[i] = withDesktopFileSpan(lines[i])
		}
	}
	return strings.Join(lines, "\n")
}

func stripLegacyGrantMarkers(line string) string {
	for _, marker := range []string{legacyGrantStart, legacyGrantEnd} {
		line = strings.ReplaceAll(line, " "+marker+" ", " ")
		line = strings.ReplaceAll(line, marker, "")
	}
	return strings.TrimRight(line, " ")
}

func withDesktopFileSpan(line string) string {
	if strings.Contains(line, " "+desktopFileSpanFlag+" ") {
		return line
	}
	head, arguments, found := strings.Cut(line, " -- ")
	if !found {
		return line
	}
	span, selects, err := countDesktopFileSpan(splitDesktopArguments(arguments))
	if err != nil || !selects {
		return line
	}
	marker := " --desktop-launch "
	position := strings.Index(head, marker)
	if position < 0 {
		return line
	}
	rest := head[position+len(marker):]
	origin, tail, split := strings.Cut(rest, " ")
	if !split {
		return line
	}
	return head[:position+len(marker)] + origin + " " + desktopFileSpanFlag + " " + span.String() + " " + tail + " -- " + arguments
}
