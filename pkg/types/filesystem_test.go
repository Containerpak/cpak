/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemPermissionRoundTrip(t *testing.T) {
	for _, permission := range []FilesystemPermission{
		{Path: "/home/user/Games", Access: "read-write"},
		{Path: "home", Access: "read-write"},
		{Path: "home/.local/share/example", Access: "read-write"},
		{Path: "host", Access: "read-only"},
		{Path: "xdg-documents", Access: "read-write"},
	} {
		encoded, err := EncodeFilesystemPermission(permission)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeFilesystemPermission(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if decoded != permission {
			t.Fatalf("got %+v, want %+v", decoded, permission)
		}
	}
}

func TestFilesystemPermissionString(t *testing.T) {
	permission := FilesystemPermission{Path: "home", Access: "read-write"}
	if got := permission.String(); got != "home (read-write)" {
		t.Fatalf("String() = %q", got)
	}
}

func TestFilesystemPermissionsRejectUnsafePaths(t *testing.T) {
	for _, permission := range []FilesystemPermission{
		{Path: "relative", Access: "read-only"},
		{Path: "home/", Access: "read-only"},
		{Path: "home/../etc", Access: "read-only"},
		{Path: "home//example", Access: "read-only"},
		{Path: "/home/../etc", Access: "read-only"},
		{Path: "/", Access: "read-only"},
		{Path: "host", Access: "read-write"},
		{Path: "/home/user", Access: "write"},
	} {
		if err := ValidateFilesystemPermissions([]FilesystemPermission{permission}); err == nil {
			t.Fatalf("accepted %+v", permission)
		}
	}
}

func TestFilesystemPermissionsRejectDuplicates(t *testing.T) {
	err := ValidateFilesystemPermissions([]FilesystemPermission{
		{Path: "/home/user", Access: "read-only"},
		{Path: "/home/user", Access: "read-write"},
	})
	if err == nil {
		t.Fatal("accepted duplicate path")
	}
}

func TestFilesystemPermissionsAcceptPortableScopes(t *testing.T) {
	if err := ValidateFilesystemPermissions([]FilesystemPermission{
		{Path: "home", Access: "read-write"},
		{Path: "home/.local/share/example", Access: "read-write"},
		{Path: "host", Access: "read-only"},
		{Path: "xdg-download", Access: "read-write"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveXDGUserDirectory(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config")
	if err := os.MkdirAll(config, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"), []byte("XDG_DOCUMENTS_DIR=\"$HOME/My Documents\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)

	source, target, err := ResolveFilesystemPermission(FilesystemPermission{Path: "xdg-documents", Access: "read-write"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "My Documents")
	if source != want || target != want {
		t.Fatalf("got %q -> %q, want %q", source, target, want)
	}
}

func TestResolveDisabledXDGUserDirectory(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "config")
	if err := os.MkdirAll(config, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "user-dirs.dirs"), []byte("XDG_DOCUMENTS_DIR=\"$HOME\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)

	_, _, err := ResolveFilesystemPermission(FilesystemPermission{Path: "xdg-documents", Access: "read-write"})
	if !errors.Is(err, ErrXDGUserDirectoryUnavailable) {
		t.Fatalf("disabled XDG directory returned %v", err)
	}
}

func TestResolveFilesystemPermission(t *testing.T) {
	tests := []struct {
		name       string
		permission FilesystemPermission
		source     string
		target     string
	}{
		{
			name:       "host",
			permission: FilesystemPermission{Path: "host", Access: "read-only"},
			source:     "/",
			target:     "/run/host",
		},
		{
			name:       "home",
			permission: FilesystemPermission{Path: "home", Access: "read-write"},
		},
		{
			name:       "home subpath",
			permission: FilesystemPermission{Path: "home/.local/share/example", Access: "read-write"},
		},
		{
			name:       "explicit path",
			permission: FilesystemPermission{Path: "/var/cache/demo", Access: "read-write"},
			source:     "/var/cache/demo",
			target:     "/var/cache/demo",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, target, err := ResolveFilesystemPermission(test.permission)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "home" || test.name == "home subpath" {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				if test.name == "home" {
					test.source, test.target = home, home
				} else {
					test.source = filepath.Join(home, ".local/share/example")
					test.target = test.source
				}
			}
			if source != test.source || target != test.target {
				t.Fatalf("got %q -> %q, want %q -> %q", source, target, test.source, test.target)
			}
		})
	}
}

func TestDecodeFilesystemPermissionsJSONRejectsUnknownFields(t *testing.T) {
	if _, err := DecodeFilesystemPermissionsJSON([]byte(`[{"path":"home","access":"read-write","unexpected":true}]`)); err == nil {
		t.Fatal("accepted unknown field")
	}
}
