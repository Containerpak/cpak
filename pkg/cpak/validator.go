package cpak

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// ManifestSchema returns the schema accepted by the runtime validator.
func ManifestSchema() *jsonschema.Schema {
	reflector := &jsonschema.Reflector{ExpandedStruct: true}
	schema := reflector.Reflect(&types.CpakManifest{})
	applyItemPatterns(schema)
	return schema
}

// ManifestV2Schema returns the editor schema for manifest version 2.
func ManifestV2Schema() *jsonschema.Schema {
	schema := ManifestSchema()
	schema.ID = jsonschema.ID(types.ManifestV2SchemaURL)
	if version, ok := schema.Properties.Get("manifest_version"); ok {
		version.Enum = nil
		version.Const = "2.0"
	}
	if override, ok := schema.Definitions["Override"]; ok && override.Properties != nil {
		removed := manifestV2RemovedOverrideFields()
		for _, field := range removed {
			override.Properties.Delete(field)
		}
		override.Required = slices.DeleteFunc(override.Required, func(field string) bool { return slices.Contains(removed, field) })
	}
	return schema
}

// ManifestV3Schema returns the editor schema for manifest version 3.
func ManifestV3Schema() *jsonschema.Schema {
	schema := ManifestSchema()
	schema.ID = jsonschema.ID(types.ManifestV3SchemaURL)
	if version, ok := schema.Properties.Get("manifest_version"); ok {
		version.Enum = nil
		version.Const = "3.0"
	}
	if schema.Properties != nil {
		schema.Properties.Delete("image_ref")
		if image, ok := schema.Properties.Get("image"); ok {
			image.Pattern = `^.+@sha256:[A-Fa-f0-9]{64}$`
			image.Description = "Digest-pinned OCI image reference"
		}
	}
	if override, ok := schema.Definitions["Override"]; ok && override.Properties != nil {
		for _, field := range manifestV3RemovedOverrideFields() {
			override.Properties.Delete(field)
		}
		override.Required = nil
	}
	return schema
}

func manifestV2RemovedOverrideFields() []string {
	return []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra", "sessionBus", "displayX11", "bluetooth"}
}

func manifestV3RemovedOverrideFields() []string {
	return []string{
		"fsHost", "fsHostEtc", "fsHostHome", "fsExtra",
		"socketX11", "socketSessionBus", "socketSystemBus", "socketAtSpiBus", "socketBluetooth",
	}
}
