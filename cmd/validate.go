/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
