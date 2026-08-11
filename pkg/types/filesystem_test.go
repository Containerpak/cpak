/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package types

import (
	"os"
	"testing"
)

func TestFilesystemPermissionRoundTrip(t *testing.T) {
	for _, permission := range []FilesystemPermission{
		{Path: "/home/user/Games", Access: "read-write"},
		{Path: "home", Access: "read-write"},
		{Path: "host", Access: "read-only"},
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

func TestFilesystemPermissionsRejectUnsafePaths(t *testing.T) {
	for _, permission := range []FilesystemPermission{
		{Path: "relative", Access: "read-only"},
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
		{Path: "host", Access: "read-only"},
	}); err != nil {
		t.Fatal(err)
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
			if test.name == "home" {
				home, err := os.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				test.source, test.target = home, home
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
