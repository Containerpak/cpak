/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const hostApplicationsTarget = "/run/cpak/host-applications"
const desktopRuntimeTarget = "/run/cpak/desktop-runtime"

func prepareHostApplicationCatalog(statePath string) (string, string, error) {
	root, mapPath := hostApplicationCatalogPaths(statePath)
	if err := os.RemoveAll(root); err != nil {
		return "", "", fmt.Errorf("reset host application catalog: %w", err)
	}
	applications := filepath.Join(root, "share", "applications")
	if err := os.MkdirAll(applications, 0755); err != nil {
		return "", "", fmt.Errorf("create host application catalog: %w", err)
	}
	launchers := hostApplicationLaunchersPath(statePath)
	if err := os.RemoveAll(launchers); err != nil {
		return "", "", fmt.Errorf("reset host application launchers: %w", err)
	}
	if err := os.Mkdir(launchers, 0700); err != nil {
		return "", "", fmt.Errorf("create host application launchers: %w", err)
	}

	dataDirectories := hostApplicationDataDirectories()
	icons := hostApplicationIconIndex(dataDirectories)
	catalog := map[string]string{}
	seen := map[string]bool{}
	for _, dataDirectory := range dataDirectories {
		directory := filepath.Join(dataDirectory, "applications")
		entries, err := os.ReadDir(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("read host applications from %s: %w", directory, err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			if seen[name] || !strings.HasSuffix(name, ".desktop") {
				continue
			}
			source := filepath.Join(directory, name)
			data, err := readDesktopEntry(source)
			if err != nil {
				continue
			}
			if !hostDesktopEntryCanLaunch(data) {
				continue
			}
			token := hostApplicationToken(source)
			icon := desktopEntryValue(data, "Icon")
			if copied, copyErr := copyHostApplicationIcon(root, token, icon, icons); copyErr == nil && copied != "" {
				icon = copied
			}
			sanitized, err := sanitizeHostDesktopEntry(data, token, icon)
			if err != nil {
				continue
			}
			if err := os.WriteFile(filepath.Join(applications, name), sanitized, 0644); err != nil {
				return "", "", fmt.Errorf("write host application %s: %w", name, err)
			}
			launcher, err := sanitizeHostApplicationLauncher(data)
			if err != nil {
				continue
			}
			launcherPath := filepath.Join(launchers, token+".desktop")
			if err := os.WriteFile(launcherPath, launcher, 0600); err != nil {
				return "", "", fmt.Errorf("write host application launcher %s: %w", name, err)
			}
			catalog[token] = launcherPath
			seen[name] = true
		}
	}

	encoded, err := json.Marshal(catalog)
	if err != nil {
		return "", "", fmt.Errorf("encode host application catalog: %w", err)
	}
	if err := os.WriteFile(mapPath, encoded, 0600); err != nil {
		return "", "", fmt.Errorf("write host application catalog map: %w", err)
	}
	return root, mapPath, nil
}

func hostApplicationCatalogPaths(statePath string) (string, string) {
	return filepath.Join(statePath, "host-applications"), filepath.Join(statePath, "host-applications.json")
}

func hostApplicationLaunchersPath(statePath string) string {
	return filepath.Join(statePath, "host-application-launchers")
}

func hostApplicationDataDirectories() []string {
	home, _ := os.UserHomeDir()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" && home != "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	dataDirectories := []string{dataHome}
	configured := os.Getenv("XDG_DATA_DIRS")
	if configured == "" {
		configured = "/usr/local/share:/usr/share"
	}
	dataDirectories = append(dataDirectories, strings.Split(configured, ":")...)
	seen := map[string]bool{}
	result := make([]string, 0, len(dataDirectories))
	for _, directory := range dataDirectories {
		directory = filepath.Clean(directory)
		if directory == "." || !filepath.IsAbs(directory) || seen[directory] {
			continue
		}
		seen[directory] = true
		result = append(result, directory)
	}
	return result
}

func readDesktopEntry(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 2<<20 {
		return nil, fmt.Errorf("invalid desktop entry")
	}
	return os.ReadFile(path)
}

func hostApplicationToken(path string) string {
	digest := sha256.Sum256([]byte(path))
	return hex.EncodeToString(digest[:])
}

func desktopEntryValue(data []byte, key string) string {
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

func hostDesktopEntryCanLaunch(data []byte) bool {
	command := desktopEntryValue(data, "TryExec")
	if command == "" {
		command = desktopEntryValue(data, "Exec")
	}
	executable, ok := desktopEntryExecutable(command)
	if !ok {
		return false
	}
	if filepath.IsAbs(executable) {
		info, err := os.Stat(executable)
		return err == nil && !info.IsDir() && info.Mode().Perm()&0111 != 0
	}
	_, err := exec.LookPath(executable)
	return err == nil
}

func desktopEntryExecutable(command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	var result strings.Builder
	quoted := false
	escaped := false
	for _, character := range command {
		switch {
		case escaped:
			result.WriteRune(character)
			escaped = false
		case character == '\\':
			escaped = true
		case character == '"':
			quoted = !quoted
		case !quoted && (character == ' ' || character == '\t'):
			if result.Len() > 0 {
				return result.String(), true
			}
		default:
			result.WriteRune(character)
		}
	}
	if escaped || quoted || result.Len() == 0 {
		return "", false
	}
	return result.String(), true
}

func sanitizeHostDesktopEntry(data []byte, token, icon string) ([]byte, error) {
	result, err := rewriteHostDesktopEntry(data, func(string) string {
		return "/usr/local/bin/cpak-launch-app " + token + " %U"
	}, icon)
	if err != nil {
		return nil, err
	}
	return append(result, []byte("X-cpak-HostApplication=true\n")...), nil
}

func sanitizeHostApplicationLauncher(data []byte) ([]byte, error) {
	return rewriteHostDesktopEntry(data, func(value string) string { return value }, "")
}

func rewriteHostDesktopEntry(data []byte, rewriteExec func(string) string, icon string) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	result := make([]string, 0, len(lines))
	inDesktopEntry := false
	foundDesktopEntry := false
	foundExec := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			if foundDesktopEntry && trimmed != "[Desktop Entry]" {
				break
			}
			inDesktopEntry = trimmed == "[Desktop Entry]"
			foundDesktopEntry = foundDesktopEntry || inDesktopEntry
			result = append(result, line)
			continue
		}
		if !inDesktopEntry {
			if !foundDesktopEntry {
				result = append(result, line)
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "Exec="):
			result = append(result, "Exec="+rewriteExec(strings.TrimSpace(strings.TrimPrefix(trimmed, "Exec="))))
			foundExec = true
		case strings.HasPrefix(trimmed, "Icon=") && icon != "":
			result = append(result, "Icon="+icon)
		case strings.HasPrefix(trimmed, "TryExec="), strings.HasPrefix(trimmed, "Actions="), strings.HasPrefix(trimmed, "DBusActivatable="):
			continue
		default:
			result = append(result, line)
		}
	}
	if !foundDesktopEntry {
		return nil, fmt.Errorf("desktop entry group is missing")
	}
	if !foundExec {
		return nil, fmt.Errorf("desktop entry command is missing")
	}
	result = append(result, "DBusActivatable=false", "")
	return []byte(strings.Join(result, "\n")), nil
}

func hostApplicationIconIndex(dataDirectories []string) map[string]string {
	icons := map[string]string{}
	for _, dataDirectory := range dataDirectories {
		for _, directory := range []string{filepath.Join(dataDirectory, "icons"), filepath.Join(dataDirectory, "pixmaps")} {
			_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return nil
				}
				extension := strings.ToLower(filepath.Ext(entry.Name()))
				if extension != ".png" && extension != ".svg" && extension != ".xpm" {
					return nil
				}
				name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				if icons[name] == "" {
					icons[name] = path
				}
				return nil
			})
		}
	}
	return icons
}

func copyHostApplicationIcon(root, token, icon string, icons map[string]string) (string, error) {
	if icon == "" {
		return "", nil
	}
	source := icon
	if !filepath.IsAbs(source) {
		source = icons[icon]
	}
	if source == "" {
		return "", nil
	}
	info, err := os.Stat(source)
	if err != nil || info.IsDir() || info.Size() > 4<<20 {
		return "", fmt.Errorf("invalid host application icon")
	}
	extension := strings.ToLower(filepath.Ext(source))
	if extension != ".png" && extension != ".svg" && extension != ".xpm" {
		return "", fmt.Errorf("unsupported host application icon")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, "share", "icons")
	if err := os.MkdirAll(directory, 0755); err != nil {
		return "", err
	}
	name := token + extension
	if err := os.WriteFile(filepath.Join(directory, name), data, 0644); err != nil {
		return "", err
	}
	return filepath.Join(hostApplicationsTarget, "share", "icons", name), nil
}
