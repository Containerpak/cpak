/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// hostWithFiles is a machine whose session configuration is written down, so a
// portable scope can be resolved without a session to read it from.
func hostWithFiles(home string, env map[string]string, files map[string]string) Host {
	host := describedHost(env, nil, nil)
	host.Home = home
	host.ReadFile = func(path string) ([]byte, error) {
		contents, ok := files[path]
		if !ok {
			return nil, fs.ErrNotExist
		}
		return []byte(contents), nil
	}
	return host
}

func TestResolveFilesystemAnswersForTheDescribedHost(t *testing.T) {
	host := hostWithFiles("/home/ada", nil, nil)
	tests := []struct {
		name       string
		permission types.FilesystemPermission
		source     string
		target     string
	}{
		{
			name:       "the host arrives somewhere it cannot be mistaken for the root",
			permission: types.FilesystemPermission{Path: "host", Access: "read-only"},
			source:     "/",
			target:     "/run/host",
		},
		{
			name:       "home is this host's home",
			permission: types.FilesystemPermission{Path: "home", Access: "read-write"},
			source:     "/home/ada",
			target:     "/home/ada",
		},
		{
			name:       "a home subpath is joined to it",
			permission: types.FilesystemPermission{Path: "home/.local/share/example", Access: "read-write"},
			source:     "/home/ada/.local/share/example",
			target:     "/home/ada/.local/share/example",
		},
		{
			name:       "an absolute path is itself",
			permission: types.FilesystemPermission{Path: "/var/cache/demo", Access: "read-write"},
			source:     "/var/cache/demo",
			target:     "/var/cache/demo",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, target, err := ResolveFilesystem(test.permission, host)
			if err != nil {
				t.Fatal(err)
			}
			if source != test.source || target != test.target {
				t.Fatalf("got %q -> %q, want %q -> %q", source, target, test.source, test.target)
			}
		})
	}
}

func TestResolveFilesystemReadsTheSessionDirectories(t *testing.T) {
	host := hostWithFiles("/home/ada", nil, map[string]string{
		"/home/ada/.config/user-dirs.dirs": "XDG_DOCUMENTS_DIR=\"$HOME/Documenti\"\n",
	})
	source, target, err := ResolveFilesystem(types.FilesystemPermission{Path: "xdg-documents", Access: "read-write"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if source != "/home/ada/Documenti" || target != "/home/ada/Documenti" {
		t.Fatalf("got %q -> %q", source, target)
	}
}

func TestResolveFilesystemFallsBackToTheUsualDirectoryName(t *testing.T) {
	host := hostWithFiles("/home/ada", nil, nil)
	source, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "xdg-pictures", Access: "read-only"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if source != "/home/ada/Pictures" {
		t.Fatalf("got %q", source)
	}
}

func TestResolveFilesystemReadsTheConfiguredConfigurationHome(t *testing.T) {
	host := hostWithFiles("/home/ada", map[string]string{"XDG_CONFIG_HOME": "/home/ada/.settings"}, map[string]string{
		"/home/ada/.settings/user-dirs.dirs": "XDG_DOWNLOAD_DIR=\"$HOME/Scaricati\"\n",
	})
	source, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "xdg-download", Access: "read-write"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if source != "/home/ada/Scaricati" {
		t.Fatalf("got %q", source)
	}
}

func TestResolveFilesystemRefusesADisabledUserDirectory(t *testing.T) {
	host := hostWithFiles("/home/ada", nil, map[string]string{
		"/home/ada/.config/user-dirs.dirs": "XDG_DOCUMENTS_DIR=\"$HOME\"\n",
	})
	_, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "xdg-documents", Access: "read-write"}, host)
	if !errors.Is(err, ErrXDGUserDirectoryUnavailable) {
		t.Fatalf("disabled XDG directory returned %v", err)
	}
}

func TestResolveFilesystemRefusesAHostWithNoHome(t *testing.T) {
	for _, home := range []string{"", "/", "relative", "/home/ada/"} {
		host := hostWithFiles(home, nil, nil)
		if _, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "home", Access: "read-write"}, host); err == nil {
			t.Fatalf("home %q resolved", home)
		}
		if _, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "xdg-music", Access: "read-write"}, host); err == nil {
			t.Fatalf("home %q resolved a user directory", home)
		}
	}
}

func TestResolveFilesystemRefusesAPermissionThatIsNotOne(t *testing.T) {
	host := hostWithFiles("/home/ada", nil, nil)
	if _, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "home/../etc", Access: "read-only"}, host); err == nil {
		t.Fatal("accepted a path that escapes home")
	}
	if _, _, err := ResolveFilesystem(types.FilesystemPermission{Path: "host", Access: "read-write"}, host); err == nil {
		t.Fatal("accepted a writable host scope")
	}
}
