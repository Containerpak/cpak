/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
)

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
			return fmt.Errorf("filesystem path must be home, host, or a clean absolute path: %q", permission.Path)
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
	case "home":
		home, homeErr := os.UserHomeDir()
		if homeErr != nil || home == "" || !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
			if homeErr != nil {
				return "", "", fmt.Errorf("resolve home filesystem scope: %w", homeErr)
			}
			return "", "", errors.New("resolve home filesystem scope")
		}
		return home, home, nil
	}
	return permission.Path, permission.Path, nil
}

func validFilesystemPath(path string) bool {
	if path == "home" || path == "host" {
		return true
	}
	return path != "/" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
