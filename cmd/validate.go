/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
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

	// Valid and complete are different questions. A manifest that says nothing
	// about a permission is not asking for it, which is easy to write by
	// accident and expensive to find out about after the package ships.
	if missing, err := types.UngrantedPermissions(data); err == nil && len(missing) > 0 {
		c.Logger.Warning(fmt.Sprintf("This manifest does not mention %d permissions, so the application will not have them: %s.", len(missing), strings.Join(missing, ", ")))
		c.Logger.Info("Nothing is granted by omission. Write the ones the application needs, and write the rest as false so the manifest says what it decided.")
	}

	c.Logger.Success("Manifest is valid.")
	return nil
}
