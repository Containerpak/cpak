/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

var embeddedAdapters = map[Backend][]byte{}

func registerEmbeddedAdapter(backend Backend, payload []byte) {
	embeddedAdapters[backend] = payload
}

func materializeEmbeddedAdapter(backend Backend) (string, error) {
	payload := embeddedAdapters[backend]
	if len(payload) == 0 {
		return "", errorsNoEmbeddedAdapter
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	directory := filepath.Join(cache, "cpak", "ui-adapters", digest)
	path := filepath.Join(directory, "cpak-ui-"+string(backend))
	if stat, statErr := os.Stat(path); statErr == nil && stat.Mode().IsRegular() && stat.Mode().Perm()&0111 != 0 {
		return path, nil
	}
	if err = os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".adapter-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err = temporary.Chmod(0700); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err = temporary.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	return path, nil
}
