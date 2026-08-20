/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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

	fvsrepo "github.com/fvs-lab/fvs2/repo"
	"github.com/mirkobrombin/cpak/pkg/logger"
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
	if available, err := c.ensureFVSLayer(digest); err != nil {
		return nil, err
	} else if available {
		return append(layers, digest), nil
	}

	artifacts := make([]string, 0, len(sources))
	fetcher := c.NewRuntimeFetcher()
	fetcher.Progress = func(message string) {
		logger.Printf("%s", message)
	}
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
	mountID, lowerDir, managerSocket, err := c.prepareFVSMount(stateDir, baseLayers)
	if err != nil {
		return nil, err
	}
	defer c.releaseFVSMount(mountID, managerSocket)

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
		"--lower-dir", lowerDir,
	}
	for i, artifact := range artifacts {
		packagePath := fmt.Sprintf("/run/cpak/runtime/%d-%s", i, RuntimeSourceFileName(sources[i]))
		args = append(args, "--extra-links", artifact+":"+packagePath)
		args = append(args, "--runtime-package", packagePath)
		args = append(args, "--runtime-installer", sources[i].Installer)
		logger.Printf("Installing runtime source %s", RuntimeSourceFileName(sources[i]))
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
	temporary, writer, err := c.beginFVSLayerSnapshot(digest, fvsrepo.SnapshotOptions{
		Message:       "runtime " + digest,
		ComputeSHA256: true,
	})
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	if err = writer.AddTree(upperDir); err != nil {
		_ = writer.Abort()
		return nil, err
	}
	if _, err = writer.Commit(); err != nil {
		return nil, err
	}
	if err = publishFVSLayer(temporary, c.fvsLayerPath(digest)); err != nil {
		return nil, err
	}
	return append(layers, digest), nil
}
