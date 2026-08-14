/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type GenSchemaCmd struct {
	Output string `cli:"output,o" help:"Output path"`

	cli.Base
}

func (c *GenSchemaCmd) Run() error {
	schema := cpak.ManifestV2Schema()

	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}
	out = append(out, '\n')

	schemaPath := c.Output
	if schemaPath == "" {
		schemaPath = "manifest.schema.json"
	}
	if parent := filepath.Dir(schemaPath); parent != "." {
		if err := os.MkdirAll(parent, 0755); err != nil {
			return fmt.Errorf("failed to create schema directory: %w", err)
		}
	}
	if err := os.WriteFile(schemaPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write schema to %s: %w", schemaPath, err)
	}

	c.Logger.Info("Schema generated at %s", schemaPath)
	return nil
}
