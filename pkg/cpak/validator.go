package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/xeipuuv/gojsonschema"
)

// ValidateManifest validates a CpakManifest against its JSON schema.
func ValidateManifest(m *types.CpakManifest) error {
	schema := ManifestSchema()

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("failed to serialize JSON schema: %w", err)
	}
	schemaLoader := gojsonschema.NewBytesLoader(schemaBytes)

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
			sb.WriteString("  - ")
			sb.WriteString(desc.String())
			sb.WriteByte('\n')
		}
		return errors.New(sb.String())
	}

	return nil
}

func ManifestSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{ExpandedStruct: true}
	return reflector.Reflect(&types.CpakManifest{})
}

func ManifestV2Schema() *jsonschema.Schema {
	schema := ManifestSchema()
	schema.ID = jsonschema.ID(types.ManifestSchemaURL)
	if version, ok := schema.Properties.Get("manifest_version"); ok {
		version.Enum = nil
		version.Const = "2.0"
	}
	if override, ok := schema.Definitions["Override"]; ok && override.Properties != nil {
		for _, legacy := range []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"} {
			override.Properties.Delete(legacy)
		}
	}
	return schema
}
