/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import "testing"

func TestManifestV2SchemaExcludesLegacyFilesystemFields(t *testing.T) {
	schema := ManifestV2Schema()
	version, ok := schema.Properties.Get("manifest_version")
	if !ok || version.Const != "2.0" {
		t.Fatalf("manifest version is not pinned to 2.0: %+v", version)
	}
	override, ok := schema.Definitions["Override"]
	if !ok {
		t.Fatal("override definition is missing")
	}
	for _, field := range []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"} {
		if _, exists := override.Properties.Get(field); exists {
			t.Fatalf("legacy field %s is present in v2 schema", field)
		}
	}
}
