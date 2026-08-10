/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"github.com/xeipuuv/gojsonschema"
)

type ValidateCmd struct {
	Manifest string `arg:"manifest" help:"Path to the manifest file"`

	cli.Base
}

func (c *ValidateCmd) Run() error {
	manifestPath := c.Manifest

	reflector := &jsonschema.Reflector{ExpandedStruct: true}
	schema := reflector.Reflect(&types.CpakManifest{})

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to serialize schema: %w", err)
	}
	schemaLoader := gojsonschema.NewBytesLoader(schemaBytes)
	documentLoader := gojsonschema.NewReferenceLoader("file://" + manifestPath)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("schema validation error: %w", err)
	}

	if !result.Valid() {
		c.Logger.Error("Manifest validation errors:")
		for _, desc := range result.Errors() {
			c.Logger.Error(" - %s", desc)
		}
		return fmt.Errorf("validation failed with %d errors", len(result.Errors()))
	}

	c.Logger.Success("Manifest is valid against the schema.")
	return nil
}
