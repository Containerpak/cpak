/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type ValidateCmd struct {
	Manifest string `arg:"manifest" help:"Path to the manifest file"`

	cli.Base
}

func (c *ValidateCmd) Run() error {
	manifestPath := c.Manifest

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	manifest, err := cpak.DecodeManifest(data)
	if err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	if err := (&cpak.Cpak{}).ValidateManifest(manifest); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}

	c.Logger.Success("Manifest is valid.")
	return nil
}
