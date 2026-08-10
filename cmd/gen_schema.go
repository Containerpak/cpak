/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type GenSchemaCmd struct {
	cli.Base
}

func (c *GenSchemaCmd) Run() error {
	reflector := &jsonschema.Reflector{
		ExpandedStruct: true,
	}
	schema := reflector.Reflect(&types.CpakManifest{})

	out, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal schema: %w", err)
	}

	schemaPath := "manifest.schema.json"
	if err := os.WriteFile(schemaPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write schema to %s: %w", schemaPath, err)
	}

	c.Logger.Info("Schema generated at %s", schemaPath)
	return nil
}
