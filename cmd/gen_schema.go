package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/spf13/cobra"
)

// NewGenSchemaCommand creates the `gen-schema` command for generating JSON
// Schema for the CpakManifest type.
func NewGenSchemaCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "gen-schema",
		Short:  "Generate JSON Schema for CpakManifest (hidden)",
		Hidden: true,
		RunE:   runGenSchema,
	}
	return cmd
}

// runGenSchema generates a JSON Schema for the CpakManifest type and writes it
// to manifest.schema.json.
func runGenSchema(cmd *cobra.Command, args []string) error {
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

	logger.Println("Schema generated at", schemaPath)
	return nil
}
