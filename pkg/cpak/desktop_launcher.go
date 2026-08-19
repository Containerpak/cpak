/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	desktopLauncherMigration = "desktop-launcher-v4"
	// The markers an exported entry used to carry. They are no longer written
	// and no longer honoured; they are named here only so a stale entry can be
	// cleaned of them.
	legacyGrantMarkerStart = "@@cpak:file-grant:start@@"
	legacyGrantMarkerEnd   = "@@cpak:file-grant:end@@"
)

func desktopLauncherPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve cpak executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve cpak executable path: %w", err)
	}
	return path, nil
}

func desktopExecArgument(value string) string {
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

func (c *Cpak) migrateDesktopLaunchers() error {
	launcher, err := desktopLauncherPath()
	if err != nil {
		return err
	}
	if filepath.Base(launcher) != "cpak" {
		return nil
	}

	directory := filepath.Join(c.Options.StorePath, "migrations")
	marker := filepath.Join(directory, desktopLauncherMigration)
	current, err := os.ReadFile(marker)
	if err == nil && string(current) == launcher {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if err := repairDesktopLaunchers(launcher); err != nil {
		return err
	}
	if err := securePrivateDirectoryUnder(c.Options.StorePath, directory); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte(launcher), 0o644)
}

func repairDesktopLaunchers(launcher string) error {
	directory := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(entry.Name(), "cpak-") && !strings.Contains(string(content), "X-cpak-Origin=") {
			continue
		}

		updated := repairDesktopLauncher(string(content), launcher)
		if updated == string(content) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := writeDesktopLauncher(path, []byte(updated), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func repairDesktopLauncher(content, launcher string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		switch {
		case line == "Exec=cpak":
			lines[i] = "Exec=" + desktopExecArgument(launcher)
		case strings.HasPrefix(line, "Exec=cpak "):
			lines[i] = "Exec=" + desktopExecArgument(launcher) + strings.TrimPrefix(line, "Exec=cpak")
		case line == "TryExec=cpak":
			lines[i] = "TryExec=" + launcher
		}
		prefix := "Exec=" + desktopExecArgument(launcher) + " run "
		if strings.HasPrefix(lines[i], prefix) && !strings.HasPrefix(lines[i], prefix+"--desktop-launch ") {
			lines[i] = prefix + "--desktop-launch " + strings.TrimPrefix(lines[i], prefix)
		}
		// Entries exported before the span existed carry the old grant markers.
		// They mean nothing now and must not linger, both because a publisher
		// could have planted one and because a reader would take them for a
		// grant that is no longer there. The migration below re-exports the
		// entry properly; this only makes sure nothing stale survives until it
		// does.
		if strings.HasPrefix(lines[i], prefix+"--desktop-launch ") {
			lines[i] = stripLegacyGrantMarkers(lines[i])
			lines[i] = withDesktopFileSpan(lines[i])
		}
	}
	return strings.Join(lines, "\n")
}

// splitDesktopArguments breaks a publisher argument list into tokens the way a
// launcher does, so cpak counts the same things the launcher will pass.
func splitDesktopArguments(value string) []string {
	tokens := []string{}
	start := 0
	quoted := false
	escaped := false
	add := func(token string) {
		if token != "" {
			tokens = append(tokens, token)
		}
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

func writeDesktopLauncher(path string, content []byte, mode os.FileMode) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cpak-desktop-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		temporary.Close()
		if err != nil {
			os.Remove(temporaryPath)
		}
	}()

	if err = temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err = temporary.Write(content); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// stripLegacyGrantMarkers removes the markers an entry was exported with before
// the span replaced them.
func stripLegacyGrantMarkers(line string) string {
	for _, marker := range []string{legacyGrantMarkerStart, legacyGrantMarkerEnd} {
		line = strings.ReplaceAll(line, " "+marker+" ", " ")
		line = strings.ReplaceAll(line, marker, "")
	}
	return strings.TrimRight(line, " ")
}

// withDesktopFileSpan gives a repaired entry the counts a freshly exported one
// carries.
//
// Repair rewrites an entry that is already on disk, and an entry exported before
// the span existed has none. Without this it would come out of repair with its
// placeholder intact and no way for cpak to know which arguments the launcher
// substituted, so the files a user opened would arrive as ordinary text and the
// application would be handed paths it cannot reach.
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
	// The flag belongs beside the other flags cpak wrote, before the binary.
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
