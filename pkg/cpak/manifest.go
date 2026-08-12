/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/mirkobrombin/cpak/pkg/types"
)

// ValidateManifest validates a manifest file, by ensuring all
// required fields are present.
func (c *Cpak) ValidateManifest(manifest *types.CpakManifest) (err error) {
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	if manifest.ManifestVersion != "1.0" && manifest.ManifestVersion != "2.0" {
		return fmt.Errorf("unsupported manifest version: %s", manifest.ManifestVersion)
	}
	if manifest.Name == "" {
		return errors.New("name is mandatory and must be populated")
	}
	if manifest.Description == "" {
		return errors.New("description is mandatory and must be populated")
	}
	if manifest.Image == "" {
		return errors.New("image is mandatory and must be populated")
	}
	if _, err = name.ParseReference(manifest.Image); err != nil {
		return fmt.Errorf("image must be a valid OCI reference: %w", err)
	}
	if manifest.ImageRef != "" && manifest.ImageRef != "source" {
		return fmt.Errorf("unsupported image_ref: %s", manifest.ImageRef)
	}
	if manifest.ImageRef == "source" {
		ref, parseErr := name.ParseReference(manifest.Image)
		if parseErr != nil {
			return fmt.Errorf("image must be a valid OCI reference: %w", parseErr)
		}
		if _, ok := ref.(name.Digest); ok {
			return errors.New("image_ref source cannot be used with an image digest")
		}
	}
	if len(manifest.Binaries) == 0 {
		return errors.New("binaries is mandatory and must be populated")
	}
	for _, dependency := range manifest.Dependencies {
		if dependency.Mode != "" && dependency.Mode != "nested" && dependency.Mode != "layer" {
			return fmt.Errorf("unsupported dependency mode for %s: %s", dependency.Origin, dependency.Mode)
		}
	}
	for _, source := range manifest.RuntimeSources {
		if err = ValidateRuntimeSource(source); err != nil {
			return err
		}
	}
	if manifest.Override.SocketBluetooth && !manifest.Override.Network {
		return errors.New("Bluetooth access requires network namespace sharing")
	}
	if manifest.ManifestVersion == "1.0" {
		if manifest.FilesystemDeclared() || len(manifest.Override.Filesystem) > 0 {
			return errors.New("filesystem permissions require manifest version 2.0")
		}
	} else {
		if fields := legacyFilesystemFields(manifest); len(fields) > 0 {
			return fmt.Errorf("legacy filesystem permissions are not supported in manifest version 2.0: %s", strings.Join(fields, ", "))
		}
		if err = types.ValidateFilesystemPermissions(manifest.Override.Filesystem); err != nil {
			return err
		}
	}
	return ValidateManifest(manifest)
}

// DecodeManifest parses a manifest without accepting unknown fields.
func DecodeManifest(content []byte) (*types.CpakManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()

	manifest := &types.CpakManifest{}
	if err := decoder.Decode(manifest); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("manifest contains multiple JSON values")
	}
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	var raw struct {
		Override map[string]json.RawMessage `json:"override"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, err
	}
	legacy := make([]string, 0, len(raw.Override))
	for _, field := range []string{"fsHost", "fsHostEtc", "fsHostHome", "fsExtra"} {
		if _, ok := raw.Override[field]; ok {
			legacy = append(legacy, field)
		}
	}
	manifest.SetLegacyFilesystemFields(legacy)
	_, filesystemDeclared := raw.Override["filesystem"]
	manifest.SetFilesystemDeclared(filesystemDeclared)
	return manifest, nil
}

// MigrateManifest upgrades a v1 manifest to v2 while preserving its effective
// permissions and replacing legacy filesystem fields with typed grants.
func MigrateManifest(manifest *types.CpakManifest) error {
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	if manifest.ManifestVersion == "2.0" {
		return nil
	}
	if manifest.ManifestVersion != "1.0" {
		return fmt.Errorf("unsupported manifest version: %s", manifest.ManifestVersion)
	}
	override := manifest.Override
	filesystem := make([]types.FilesystemPermission, 0, len(override.FsExtra)+3)
	if override.FsHost {
		filesystem = append(filesystem, types.FilesystemPermission{Path: "host", Access: "read-only"})
	}
	if override.FsHostEtc {
		filesystem = append(filesystem, types.FilesystemPermission{Path: "/etc", Access: "read-only"})
	}
	if override.FsHostHome {
		filesystem = append(filesystem, types.FilesystemPermission{Path: "home", Access: "read-write"})
	}
	for _, path := range override.FsExtra {
		filesystem = append(filesystem, types.FilesystemPermission{Path: path, Access: "read-write"})
	}
	if err := types.ValidateFilesystemPermissions(filesystem); err != nil {
		return fmt.Errorf("migrate filesystem permissions: %w", err)
	}
	override.Filesystem = filesystem
	override.FsHost = false
	override.FsHostEtc = false
	override.FsHostHome = false
	override.FsExtra = nil
	manifest.ManifestVersion = "2.0"
	manifest.Override = override
	manifest.SetLegacyFilesystemFields(nil)
	manifest.SetFilesystemDeclared(len(filesystem) > 0)
	return nil
}

func legacyFilesystemFields(manifest *types.CpakManifest) []string {
	fields := manifest.LegacyFilesystemFields()
	if manifest.Override.FsHost {
		fields = append(fields, "fsHost")
	}
	if manifest.Override.FsHostEtc {
		fields = append(fields, "fsHostEtc")
	}
	if manifest.Override.FsHostHome {
		fields = append(fields, "fsHostHome")
	}
	if len(manifest.Override.FsExtra) > 0 {
		fields = append(fields, "fsExtra")
	}
	sort.Strings(fields)
	return uniqueManifestFields(fields)
}

func uniqueManifestFields(fields []string) []string {
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(result) == 0 || result[len(result)-1] != field {
			result = append(result, field)
		}
	}
	return result
}

// fetchManifest fetches the manifest file from the given origin.
func (c *Cpak) FetchManifest(origin, branch, release, commit string) (manifest *types.CpakManifest, err error) {
	// remove trailing .git if present
	origin = strings.TrimSuffix(origin, ".git")

	// if any protocol is specified, we release a failuer since we force
	// the use of https and the user should not specify any protocol
	if strings.Contains(origin, "://") {
		return nil, fmt.Errorf("do not specify any protocol in the origin repository URL")
	}

	repoProvider, err := NewRepoProvider(origin, c.Options.ManifestsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create repo provider: %w", err)
	}

	var manifestContent []byte
	switch {
	case branch != "":
		manifestContent, err = repoProvider.GetFileInBranch("cpak.json", branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	case release != "":
		manifestContent, err = repoProvider.GetFileInRelease("cpak.json", release)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	case commit != "":
		manifestContent, err = repoProvider.GetFileInCommit("cpak.json", commit)
		if err != nil {
			return nil, fmt.Errorf("failed to get manifest file: %w", err)
		}
	default:
		return nil, fmt.Errorf("no branch, release or commit specified")
	}

	manifest, err = DecodeManifest(manifestContent)
	if err != nil {
		return nil, fmt.Errorf("failed to decode manifest file: %w", err)
	}

	return manifest, nil
}
