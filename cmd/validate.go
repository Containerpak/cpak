package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/spf13/cobra"
	"github.com/xeipuuv/gojsonschema"
)

// NewValidateCommand creates the `validate` command for verifying a cpak.json
// manifest against the JSON Schema.
func NewValidateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate [manifest]",
		Short: "Validate a cpak.json manifest against manifest.schema.json",
		Args:  cobra.ExactArgs(1),
		RunE:  runValidate,
	}
	return cmd
}

// runValidate checks the provided manifest against the JSON Schema and reports
// any validation errors.
func runValidate(cmd *cobra.Command, args []string) error {
	manifestPath := args[0]

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
		logger.Println("Manifest validation errors:")
		for _, desc := range result.Errors() {
			logger.Printf(" - %s", desc)
		}
		return fmt.Errorf("validation failed with %d errors", len(result.Errors()))
	}

	logger.Println("Manifest is valid against the schema.")
	return nil
}
