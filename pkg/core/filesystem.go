/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// ErrXDGUserDirectoryUnavailable is answered for a user directory the session
// turned off, which is written by pointing it at the home directory itself. It
// is not a failure: an application that asked for the pictures directory of a
// user who has none simply does not get one.
var ErrXDGUserDirectoryUnavailable = errors.New("XDG user directory is unavailable")

// ResolveFilesystem answers where a filesystem permission lands on a host: the
// directory the contents come from, and the path the application finds them at.
//
// The two are the same everywhere except the host scope, which is the whole
// filesystem offered at /run/host, so an application that was given the host
// cannot mistake it for its own root. The portable scopes are the reason this
// takes a host at all: home and the XDG user directories name a place on a
// machine rather than a path, and what they name is read off that machine's
// home directory and session configuration.
func ResolveFilesystem(permission types.FilesystemPermission, h Host) (source, target string, err error) {
	if err = types.ValidateFilesystemPermissions([]types.FilesystemPermission{permission}); err != nil {
		return "", "", err
	}
	if permission.Path == "host" {
		return "/", "/run/host", nil
	}
	if relative, ok := types.HomeFilesystemSubpath(permission.Path); ok {
		home, homeErr := usableHome(h)
		if homeErr != nil {
			return "", "", fmt.Errorf("resolve home filesystem scope: %w", homeErr)
		}
		if relative == "." {
			return home, home, nil
		}
		resolved := path.Join(home, relative)
		return resolved, resolved, nil
	}
	if _, _, ok := types.XDGUserDirectory(permission.Path); ok {
		resolved, resolveErr := xdgUserDirectory(permission.Path, h)
		if resolveErr != nil {
			return "", "", resolveErr
		}
		return resolved, resolved, nil
	}
	return permission.Path, permission.Path, nil
}

// xdgUserDirectory answers the directory a portable scope names on this host.
// The session's own configuration decides it where there is one, and the name
// every desktop starts from decides it where there is not.
func xdgUserDirectory(scope string, h Host) (string, error) {
	home, err := usableHome(h)
	if err != nil {
		return "", fmt.Errorf("resolve XDG user directory: %w", err)
	}
	key, fallback, ok := types.XDGUserDirectory(scope)
	if !ok {
		return "", fmt.Errorf("unknown XDG user directory: %s", scope)
	}
	configHome := h.env("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = path.Join(home, ".config")
	}
	resolved := path.Join(home, fallback)
	data, readErr := h.readFile(path.Join(configHome, "user-dirs.dirs"))
	if readErr == nil {
		if configured, found := ParseXDGUserDirectory(data, key, home); found {
			resolved = configured
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return "", fmt.Errorf("read XDG user directories: %w", readErr)
	}
	resolved = path.Clean(resolved)
	if resolved == home || resolved == "/" || !path.IsAbs(resolved) {
		return "", fmt.Errorf("%w: %s", ErrXDGUserDirectoryUnavailable, scope)
	}
	return resolved, nil
}

// ParseXDGUserDirectory reads one directory out of a user-dirs.dirs file, the
// way the desktop that wrote it does: a shell assignment whose value is quoted
// and written against $HOME.
func ParseXDGUserDirectory(data []byte, key, home string) (string, bool) {
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
			value = path.Join(home, strings.TrimPrefix(value, "$HOME/"))
		}
		return value, true
	}
	return "", false
}

// usableHome answers the home directory only where it is one. A home nobody
// resolved, or one that is the root of the filesystem, would turn a scope that
// means "this user's files" into the whole machine.
func usableHome(h Host) (string, error) {
	if h.Home == "" || !path.IsAbs(h.Home) || path.Clean(h.Home) != h.Home || h.Home == "/" {
		return "", errors.New("the host has no home directory")
	}
	return h.Home, nil
}
