/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var xdgDirectoryKeys = map[string]string{
	"xdg-desktop":      "DESKTOP",
	"xdg-documents":    "DOCUMENTS",
	"xdg-download":     "DOWNLOAD",
	"xdg-music":        "MUSIC",
	"xdg-pictures":     "PICTURES",
	"xdg-public-share": "PUBLICSHARE",
	"xdg-templates":    "TEMPLATES",
	"xdg-videos":       "VIDEOS",
}

var xdgDirectoryDefaults = map[string]string{
	"DESKTOP":     "Desktop",
	"DOCUMENTS":   "Documents",
	"DOWNLOAD":    "Downloads",
	"MUSIC":       "Music",
	"PICTURES":    "Pictures",
	"PUBLICSHARE": "Public",
	"TEMPLATES":   "Templates",
	"VIDEOS":      "Videos",
}

var ErrXDGUserDirectoryUnavailable = errors.New("XDG user directory is unavailable")

func EncodeFilesystemPermission(permission FilesystemPermission) (string, error) {
	if err := ValidateFilesystemPermissions([]FilesystemPermission{permission}); err != nil {
		return "", err
	}
	data, err := json.Marshal(permission)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func DecodeFilesystemPermission(encoded string) (FilesystemPermission, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return FilesystemPermission{}, fmt.Errorf("decode filesystem permission: %w", err)
	}
	permission := FilesystemPermission{}
	if err = decodeFilesystemJSON(data, &permission); err != nil {
		return FilesystemPermission{}, err
	}
	if err = ValidateFilesystemPermissions([]FilesystemPermission{permission}); err != nil {
		return FilesystemPermission{}, err
	}
	return permission, nil
}

func DecodeFilesystemPermissionsJSON(data []byte) ([]FilesystemPermission, error) {
	permissions := []FilesystemPermission{}
	if err := decodeFilesystemJSON(data, &permissions); err != nil {
		return nil, err
	}
	if err := ValidateFilesystemPermissions(permissions); err != nil {
		return nil, err
	}
	return permissions, nil
}

func decodeFilesystemJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode filesystem permission: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("filesystem permission contains multiple JSON values")
	}
	return nil
}

func ValidateFilesystemPermissions(permissions []FilesystemPermission) error {
	paths := make(map[string]struct{}, len(permissions))
	for _, permission := range permissions {
		if !validFilesystemPath(permission.Path) {
			return fmt.Errorf("filesystem path must be home, a home subpath, host, an XDG user directory, or a clean absolute path: %q", permission.Path)
		}
		if permission.Access != "read-only" && permission.Access != "read-write" {
			return fmt.Errorf("filesystem access must be read-only or read-write: %q", permission.Access)
		}
		if permission.Path == "host" && permission.Access != "read-only" {
			return errors.New("filesystem host scope can only be read-only")
		}
		if _, exists := paths[permission.Path]; exists {
			return fmt.Errorf("filesystem path is declared more than once: %s", permission.Path)
		}
		paths[permission.Path] = struct{}{}
	}
	return nil
}

func ResolveFilesystemPermission(permission FilesystemPermission) (source, target string, err error) {
	if err = ValidateFilesystemPermissions([]FilesystemPermission{permission}); err != nil {
		return "", "", err
	}
	switch permission.Path {
	case "host":
		return "/", "/run/host", nil
	}
	if relative, ok := homeFilesystemSubpath(permission.Path); ok {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
			if homeErr != nil {
				return "", "", fmt.Errorf("resolve home filesystem scope: %w", homeErr)
			}
			return "", "", errors.New("resolve home filesystem scope")
		}
		if relative == "." {
			return home, home, nil
		}
		path := filepath.Join(home, relative)
		return path, path, nil
	}
	if _, ok := xdgDirectoryKeys[permission.Path]; ok {
		path, resolveErr := resolveXDGUserDirectory(permission.Path)
		if resolveErr != nil {
			return "", "", resolveErr
		}
		return path, path, nil
	}
	return permission.Path, permission.Path, nil
}

func validFilesystemPath(path string) bool {
	if path == "host" {
		return true
	}
	if _, ok := homeFilesystemSubpath(path); ok {
		return true
	}
	if _, ok := xdgDirectoryKeys[path]; ok {
		return true
	}
	return path != "/" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func homeFilesystemSubpath(path string) (string, bool) {
	if path == "home" {
		return ".", true
	}
	if !strings.HasPrefix(path, "home/") {
		return "", false
	}
	relative := strings.TrimPrefix(path, "home/")
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func resolveXDGUserDirectory(scope string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		if err != nil {
			return "", fmt.Errorf("resolve XDG user directory: %w", err)
		}
		return "", errors.New("resolve XDG user directory")
	}
	key, ok := xdgDirectoryKeys[scope]
	if !ok {
		return "", fmt.Errorf("unknown XDG user directory: %s", scope)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	path := filepath.Join(home, xdgDirectoryDefaults[key])
	data, readErr := os.ReadFile(filepath.Join(configHome, "user-dirs.dirs"))
	if readErr == nil {
		if configured, found := parseXDGUserDirectory(data, key, home); found {
			path = configured
		}
	} else if !os.IsNotExist(readErr) {
		return "", fmt.Errorf("read XDG user directories: %w", readErr)
	}
	path = filepath.Clean(path)
	if path == home || path == "/" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %s", ErrXDGUserDirectoryUnavailable, scope)
	}
	return path, nil
}

func parseXDGUserDirectory(data []byte, key, home string) (string, bool) {
	prefix := "XDG_" + key + "_DIR="
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value, err := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		if err != nil {
			return "", false
		}
		if value == "$HOME" {
			return home, true
		}
		if strings.HasPrefix(value, "$HOME/") {
			value = filepath.Join(home, strings.TrimPrefix(value, "$HOME/"))
		}
		return value, true
	}
	return "", false
}
