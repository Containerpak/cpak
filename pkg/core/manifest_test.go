/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const exampleManifest = `{
	"manifest_version": "3.0",
	"name": "Example",
	"description": "An example application",
	"image": "ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"binaries": ["/usr/bin/example"],
	"idle_time": 5,
	"override": {"socketWayland": true}
}`

func TestAValidManifestIsAccepted(t *testing.T) {
	manifest, err := DecodeManifest([]byte(exampleManifest))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestAManifestWithoutAnImageIsRefused(t *testing.T) {
	manifest, err := DecodeManifest([]byte(strings.Replace(exampleManifest, `"image": "ghcr.io/example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",`, `"image": "",`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("a manifest with no image was accepted")
	}
}

func TestVersionTwoRefusesTheLegacyFilesystemFields(t *testing.T) {
	content := strings.Replace(exampleManifest, `"manifest_version": "3.0"`, `"manifest_version": "2.0"`, 1)
	manifest, err := DecodeManifest([]byte(strings.Replace(content, `"override": {"socketWayland": true}`, `"override": {"fsHostHome": true}`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	err = ValidateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "fsHostHome") {
		t.Fatalf("validate: got %v, want a refusal naming fsHostHome", err)
	}
}

func TestAFieldNobodyWroteIsNotAFieldSetToFalse(t *testing.T) {
	written, err := DecodeManifest([]byte(strings.Replace(exampleManifest, `"override": {"socketWayland": true}`, `"override": {"fsHostHome": false}`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fields := LegacyFilesystemFields(written); len(fields) != 1 || fields[0] != "fsHostHome" {
		t.Fatalf("legacy fields: got %v, want fsHostHome, which the manifest wrote", fields)
	}
	silent, err := DecodeManifest([]byte(exampleManifest))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if fields := LegacyFilesystemFields(silent); len(fields) != 0 {
		t.Fatalf("legacy fields: got %v for a manifest that wrote none", fields)
	}
}

func TestMigrationTurnsLegacyFieldsIntoTypedPermissions(t *testing.T) {
	manifest, err := DecodeManifest([]byte(`{
		"manifest_version": "1.0",
		"name": "Old",
		"description": "A v1 application",
		"image": "ghcr.io/example/old:latest",
		"binaries": ["/usr/bin/old"],
		"override": {"fsHost": true, "fsHostHome": true, "fsExtra": ["/opt/data"], "allowedHostCommands": ["xdg-open"]}
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := MigrateManifest(manifest); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if manifest.ManifestVersion != "2.0" {
		t.Fatalf("version: got %s, want 2.0", manifest.ManifestVersion)
	}
	want := map[string]string{"host": "read-only", "home": "read-write", "/opt/data": "read-write"}
	if len(manifest.Override.Filesystem) != len(want) {
		t.Fatalf("filesystem: got %v, want %v", manifest.Override.Filesystem, want)
	}
	for _, permission := range manifest.Override.Filesystem {
		if want[permission.Path] != permission.Access {
			t.Fatalf("filesystem %s: got %s, want %s", permission.Path, permission.Access, want[permission.Path])
		}
	}
	if !manifest.Override.OpenURI {
		t.Fatal("the xdg-open host command did not become the openURI permission")
	}
	if len(manifest.Override.AllowedHostCommands) != 0 {
		t.Fatalf("host commands: got %v, want none left", manifest.Override.AllowedHostCommands)
	}
}

func TestAHostCommandWithNoProviderIsRefused(t *testing.T) {
	manifest, err := DecodeManifest([]byte(strings.Replace(exampleManifest,
		`"override": {"socketWayland": true}`,
		`"override": {"allowedHostCommands": ["rm"]}`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := MigrateManifest(manifest); err == nil {
		t.Fatal("a host command with no typed provider was migrated away silently")
	}
}

func TestVersionThreeRefusesRawBusAndX11Permissions(t *testing.T) {
	manifest, err := DecodeManifest([]byte(strings.Replace(exampleManifest,
		`"override": {"socketWayland": true}`,
		`"override": {"socketBluetooth": true, "socketX11": true}`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	err = ValidateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "removed fields") {
		t.Fatalf("validate: got %v, want a v3 removed-fields refusal", err)
	}
}

func TestVersionThreeAcceptsIsolatedDesktopCapabilities(t *testing.T) {
	manifest, err := DecodeManifest([]byte(strings.Replace(exampleManifest,
		`"override": {"socketWayland": true}`,
		`"override": {"displayX11": true, "bluetooth": true}`, 1)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestAnUnknownFieldIsRefused(t *testing.T) {
	if _, err := DecodeManifest([]byte(strings.Replace(exampleManifest, `"name": "Example",`, `"name": "Example", "nmae": "Example",`, 1))); err == nil {
		t.Fatal("a misspelled field was accepted, which loses whatever it meant")
	}
}

func TestApplicationServicesUseExportedBinaries(t *testing.T) {
	manifest, err := DecodeManifest([]byte(strings.Replace(exampleManifest,
		`"binaries": ["/usr/bin/example"],`,
		`"binaries": ["/usr/bin/example"], "services": {"server": {"binary": "/usr/bin/example", "arguments": ["serve"]}},`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateManifest(manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Services["server"] = types.ApplicationService{Binary: "/usr/bin/other"}
	if err = ValidateManifest(manifest); err == nil || !strings.Contains(err.Error(), "binary is not exported") {
		t.Fatalf("service validation: %v", err)
	}
}
