/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func loadManifest(path string) (*types.CpakManifest, error) {
	if path == "" {
		path = "cpak.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	manifest, err := cpak.DecodeManifest(data)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	if err = (&cpak.Cpak{}).ValidateManifest(manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return manifest, nil
}

func loadManifestLock(path string) (types.ManifestLock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.ManifestLock{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	lock := types.ManifestLock{}
	if err = decoder.Decode(&lock); err != nil {
		return types.ManifestLock{}, fmt.Errorf("invalid lock file: %w", err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return types.ManifestLock{}, errors.New("invalid lock file: multiple JSON values")
	}
	return lock, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".cpak-json-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func inferredLockPath(manifestPath, explicit string) string {
	if explicit != "" {
		return explicit
	}
	path := filepath.Join(filepath.Dir(manifestPath), "cpak.lock.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func localPackageOrigin(mode string, manifest *types.CpakManifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "local.cpak/" + mode + "/" + fmt.Sprintf("%x", digest[:])[:16], nil
}

func applyLockedImage(manifest *types.CpakManifest, lock types.ManifestLock) *types.CpakManifest {
	copy := *manifest
	copy.Image = lock.Root.ResolvedImage
	copy.ImageRef = ""
	return &copy
}
