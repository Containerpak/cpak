/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/xeipuuv/gojsonschema"
)

type itemPattern struct {
	definition string
	property   string
	pattern    string
}

var manifestItemPatterns = []itemPattern{
	{property: "binaries", pattern: `^/[^\x00-\x1f\x7f-\x9f]+$`},
	{property: "desktop_entries", pattern: `^.+\.desktop$`},
	{definition: "Override", property: "env", pattern: `^[A-Za-z_][A-Za-z0-9_]*=`},
	{definition: "Override", property: "allowedHostCommands", pattern: `^[A-Za-z0-9_-]+$`},
}

func ValidateManifestSchema(manifest *types.CpakManifest) error {
	reflector := &jsonschema.Reflector{ExpandedStruct: true}
	schema := reflector.Reflect(&types.CpakManifest{})
	applyItemPatterns(schema)

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to serialize JSON schema: %w", err)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	result, err := gojsonschema.Validate(
		gojsonschema.NewBytesLoader(schemaBytes),
		gojsonschema.NewBytesLoader(manifestBytes),
	)
	if err != nil {
		return fmt.Errorf("schema validation error: %w", err)
	}
	if result.Valid() {
		return nil
	}

	var message strings.Builder
	message.WriteString("manifest validation failed:\n")
	for _, description := range result.Errors() {
		message.WriteString("  - ")
		message.WriteString(description.String())
		message.WriteByte('\n')
	}
	return errors.New(message.String())
}

func applyItemPatterns(schema *jsonschema.Schema) {
	for _, item := range manifestItemPatterns {
		if items := arrayItemSchema(schema, item.definition, item.property); items != nil {
			items.Pattern = item.pattern
		}
	}
}

func arrayItemSchema(schema *jsonschema.Schema, definition, property string) *jsonschema.Schema {
	holder := schema
	if definition != "" {
		holder = schema.Definitions[definition]
	}
	if holder == nil || holder.Properties == nil {
		return nil
	}
	field, ok := holder.Properties.Get(property)
	if !ok || field == nil {
		return nil
	}
	return field.Items
}
