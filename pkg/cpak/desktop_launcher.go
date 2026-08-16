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
	desktopLauncherMigration = "desktop-launcher-v3"
	desktopFileArgumentStart = "@@cpak:file-grant:start@@"
	desktopFileArgumentEnd   = "@@cpak:file-grant:end@@"
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
	if err := os.MkdirAll(directory, 0o755); err != nil {
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
		if strings.HasPrefix(lines[i], prefix+"--desktop-launch ") {
			lines[i] = markDesktopFileArguments(lines[i])
		}
	}
	return strings.Join(lines, "\n")
}

func markDesktopFileArguments(value string) string {
	tokens := make([]string, 0, strings.Count(value, " ")+1)
	start := 0
	quoted := false
	escaped := false
	writeToken := func(token string) {
		if token == "" {
			return
		}
		field := token
		if len(field) >= 2 && field[0] == '"' && field[len(field)-1] == '"' {
			field = field[1 : len(field)-1]
		}
		switch {
		case field == desktopFileArgumentStart, field == desktopFileArgumentEnd:
			return
		case field == "%f" || field == "%F" || field == "%u" || field == "%U":
			tokens = append(tokens, desktopFileArgumentStart, token, desktopFileArgumentEnd)
		default:
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
			writeToken(value[start:index])
			start = index + 1
		}
	}
	writeToken(value[start:])
	return strings.Join(tokens, " ")
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
