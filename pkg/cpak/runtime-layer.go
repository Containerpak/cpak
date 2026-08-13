/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func RuntimeLayerDigest(baseLayers []string, sources []types.RuntimeSource) string {
	hash := sha256.New()
	writePart := func(value string) {
		_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
		_, _ = hash.Write([]byte{':'})
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{'\n'})
	}
	for _, layer := range baseLayers {
		writePart(layer)
	}
	for _, source := range sources {
		writePart(source.Name)
		writePart(source.URL)
		writePart(strings.ToLower(source.SHA256))
		writePart(strconv.FormatInt(source.Size, 10))
		writePart(source.Installer)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (c *Cpak) BuildRuntimeLayers(baseLayers []string, sources []types.RuntimeSource) ([]string, error) {
	layers := append([]string{}, baseLayers...)
	if len(sources) == 0 {
		return layers, nil
	}

	digest := RuntimeLayerDigest(baseLayers, sources)
	layerPath := c.GetInStoreDir("layers", digest)
	if info, err := os.Stat(layerPath); err == nil && info.IsDir() {
		return append(layers, digest), nil
	}

	artifacts := make([]string, 0, len(sources))
	fetcher := c.NewRuntimeFetcher()
	for _, source := range sources {
		artifact, err := fetcher.Fetch(source)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}

	stateDir, err := os.MkdirTemp(c.Options.StoreStatesPath, ".runtime-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stateDir)
	rootfs, err := os.MkdirTemp(c.Options.StoreContainersPath, ".runtime-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(rootfs)
	for _, dir := range []string{"up", "work"} {
		if err = os.MkdirAll(filepath.Join(stateDir, dir), 0755); err != nil {
			return nil, err
		}
	}

	cpakBinary, err := getCpakBinary()
	if err != nil {
		return nil, err
	}
	args := []string{
		"spawn",
		"--build-layer",
		"--rootfs", rootfs,
		"--state-dir", stateDir,
		"--layers", strings.Join(baseLayers, "|") + "|",
		"--layers-dir", c.GetInStoreDir("layers"),
	}
	for i, artifact := range artifacts {
		packagePath := fmt.Sprintf("/run/cpak/runtime/%d-%s", i, RuntimeSourceFileName(sources[i]))
		args = append(args, "--extra-links", artifact+":"+packagePath)
		args = append(args, "--runtime-package", packagePath)
	}

	cmd := nativeNamespaceCommand(cpakBinary, args, namespaceOptions{
		IsolateNetwork: true,
		ShareProcesses: false,
		IsolateCgroup:  true,
	})
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err = cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to build runtime layer: %w", err)
	}

	upperDir := filepath.Join(stateDir, "up")
	if err = os.Rename(upperDir, layerPath); err != nil {
		if info, statErr := os.Stat(layerPath); statErr == nil && info.IsDir() {
			return append(layers, digest), nil
		}
		return nil, err
	}
	return append(layers, digest), nil
}
