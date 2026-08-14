/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DevCmd struct {
	Manifest string   `arg:"manifest" help:"Path to cpak.json"`
	Extra    []string `arg:"extra" help:"Arguments passed to the application"`
	Lock     string   `cli:"lock" help:"Path to cpak.lock.json"`
	Origin   string   `cli:"origin" help:"Repository origin for relative dependencies"`
	Binary   string   `cli:"binary" help:"Binary to launch"`
	Verbose  bool     `cli:"verbose,v" help:"Enable verbose output"`

	cli.Base
}

func (c *DevCmd) Run() error {
	manifestPath := c.Manifest
	if manifestPath == "" {
		manifestPath = "cpak.json"
	}
	manifest, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	binary := c.Binary
	if binary == "" {
		if len(manifest.Binaries) == 0 {
			return fmt.Errorf("manifest has no binary to launch")
		}
		binary = manifest.Binaries[0]
	}
	err = runLocalPackage(localPackageRequest{
		Mode:         "dev",
		ManifestPath: manifestPath,
		LockPath:     c.Lock,
		Origin:       c.Origin,
		Binary:       binary,
		Extra:        c.Extra,
		Verbose:      c.Verbose,
		Launch:       true,
	})
	var exitErr *types.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("development run failed: %w", err)
	}
	return err
}
