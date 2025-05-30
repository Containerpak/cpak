package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/xeipuuv/gojsonschema"
)

// ValidateManifest validates a CpakManifest against its JSON schema.
func ValidateManifest(m *types.CpakManifest) error {
	exeDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return fmt.Errorf("cannot locate executable dir: %w", err)
	}
	schemaPath := filepath.Join(exeDir, "manifest.schema.json")
	if _, err := os.Stat(schemaPath); err != nil {
		return fmt.Errorf("schema file not found at %s: %w", schemaPath, err)
	}

	schemaLoader := gojsonschema.NewReferenceLoader("file://" + schemaPath)

	manifestBytes, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	documentLoader := gojsonschema.NewBytesLoader(manifestBytes)

	result, err := gojsonschema.Validate(schemaLoader, documentLoader)
	if err != nil {
		return fmt.Errorf("schema validation error: %w", err)
	}
	if !result.Valid() {
		var sb strings.Builder
		sb.WriteString("manifest validation failed:\n")
		for _, desc := range result.Errors() {
			sb.WriteString("  • ")
			sb.WriteString(desc.String())
			sb.WriteByte('\n')
		}
		return errors.New(sb.String())
	}
	return nil
}
