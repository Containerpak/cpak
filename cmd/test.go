/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"errors"
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type TestCmd struct {
	Manifest string   `arg:"manifest" help:"Path to cpak.json"`
	Extra    []string `arg:"extra" help:"Arguments passed to the application"`
	Lock     string   `cli:"lock" help:"Path to cpak.lock.json"`
	Origin   string   `cli:"origin" help:"Repository origin for relative dependencies"`
	Binary   string   `cli:"binary" help:"Optional binary to run after validation"`
	Verbose  bool     `cli:"verbose,v" help:"Enable verbose output"`

	cli.Base
}

func (c *TestCmd) Run() error {
	err := runLocalPackage(localPackageRequest{
		Mode:         "test",
		ManifestPath: c.Manifest,
		LockPath:     c.Lock,
		Origin:       c.Origin,
		Binary:       c.Binary,
		Extra:        c.Extra,
		Verbose:      c.Verbose,
		Launch:       c.Binary != "",
	})
	var exitErr *types.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("package test failed: %w", err)
	}
	if err != nil {
		return err
	}
	c.Logger.Success("Package test passed.")
	return nil
}
