/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/types"
)

var sessionIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)

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
	if _, err = oci.ParseReference(manifest.Image); err != nil {
		return fmt.Errorf("image must be a valid OCI reference: %w", err)
	}
	if manifest.ImageRef != "" && manifest.ImageRef != "source" {
		return fmt.Errorf("unsupported image_ref: %s", manifest.ImageRef)
	}
	if manifest.ImageRef == "source" {
		ref, parseErr := oci.ParseReference(manifest.Image)
		if parseErr != nil {
			return fmt.Errorf("image must be a valid OCI reference: %w", parseErr)
		}
		if ref.IsDigest {
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
	if err = migrateLegacyHostCommands(&manifest.Override); err != nil {
		return err
	}
	if err = types.ValidateHostActions(manifest.Override.HostActions); err != nil {
		return err
	}
	if err = types.ValidateFilePickerGrant(manifest.Override.FilePicker); err != nil {
		return err
	}
	if err = validateSessions(manifest); err != nil {
		return err
	}
	if manifest.Override.SocketBluetooth && !manifest.Override.Network {
		return errors.New("Bluetooth access requires network namespace sharing")
	}
	if manifest.ManifestVersion == "1.0" {
		if len(manifest.Override.HostActions) > 0 {
			return errors.New("host actions require manifest version 2.0")
		}
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

func validateSessions(manifest *types.CpakManifest) error {
	binaries := make(map[string]bool, len(manifest.Binaries))
	for _, binary := range manifest.Binaries {
		binaries[binary] = true
	}
	ids := make(map[string]bool, len(manifest.Sessions))
	for _, session := range manifest.Sessions {
		if len(session.ID) == 0 || len(session.ID) > 96 || !sessionIDPattern.MatchString(session.ID) {
			return fmt.Errorf("invalid session id: %q", session.ID)
		}
		if ids[session.ID] {
			return fmt.Errorf("session is declared more than once: %s", session.ID)
		}
		ids[session.ID] = true
		if err := validateSessionText(session.Name, 80); err != nil {
			return fmt.Errorf("invalid session name for %s", session.ID)
		}
		if session.Description != "" {
			if err := validateSessionText(session.Description, 160); err != nil {
				return fmt.Errorf("invalid session description for %s", session.ID)
			}
		}
		if session.Kind != "desktop" && session.Kind != "kiosk" {
			return fmt.Errorf("unsupported session kind for %s: %s", session.ID, session.Kind)
		}
		if !binaries[session.Entrypoint] {
			return fmt.Errorf("session %s entrypoint is not an exported binary", session.ID)
		}
		if session.Override.AsRoot {
			return fmt.Errorf("session %s cannot run as root", session.ID)
		}
		if len(session.Override.AllowedHostCommands) > 0 {
			return fmt.Errorf("session %s cannot declare host commands", session.ID)
		}
		if err := types.ValidateHostActions(session.Override.HostActions); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
		if err := types.ValidateFilePickerGrant(session.Override.FilePicker); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
		if err := types.ValidateFilesystemPermissions(session.Override.Filesystem); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
	}
	return nil
}

func validateSessionText(value string, limit int) error {
	if value == "" || len(value) > limit || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid session text")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return errors.New("invalid session text")
		}
	}
	return nil
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
		return migrateLegacyHostCommands(&manifest.Override)
	}
	if manifest.ManifestVersion != "1.0" {
		return fmt.Errorf("unsupported manifest version: %s", manifest.ManifestVersion)
	}
	// What the legacy fields stand for is written down once, in pkg/types,
	// because the migration is not the only reader of it: the ledger has to
	// recognise a policy it recorded in the legacy shape and one offered in the
	// migrated shape as the same restriction, and an update has to weigh a
	// stored override against a fetched one without reporting the change of
	// shape as a change of permission. A second copy of the table here would be
	// a second answer waiting to disagree with the first.
	//
	// The list stays absent when the manifest granted nothing, so migrating a
	// manifest that named no mount leaves an override equal to the one already
	// installed rather than one that merely reads the same. Override.Diff
	// compares the fields whole, and an empty list is not the absent one.
	override := manifest.Override.WithMigratedFilesystem()
	if err := types.ValidateFilesystemPermissions(override.Filesystem); err != nil {
		return fmt.Errorf("migrate filesystem permissions: %w", err)
	}
	if err := migrateLegacyHostCommands(&override); err != nil {
		return err
	}
	manifest.ManifestVersion = "2.0"
	manifest.Override = override
	manifest.SetLegacyFilesystemFields(nil)
	manifest.SetFilesystemDeclared(len(override.Filesystem) > 0)
	return nil
}

// installedOverride is the override an installation applies: the legacy
// filesystem fields of a v1 manifest turned into the typed grants the rest of
// cpak reads. Nothing downstream reads fsHostHome, fsHost, fsHostEtc or
// fsExtra as permissions; they become plain mounts that never meet the typed
// grants, so they are neither masked nor weighed against a ceiling.
//
// The manifest itself is left exactly as it was published. It is what the lock
// records a package by and what a publisher's signature is taken over, both of
// them after validation, so a manifest that changed while cpak read it would
// hash to a package nobody ever signed and to a lock nobody ever wrote.
func installedOverride(manifest *types.CpakManifest) (types.Override, error) {
	migrated := *manifest
	if err := MigrateManifest(&migrated); err != nil {
		return types.Override{}, err
	}
	return migrated.Override, nil
}

// installedRecordOverride is the override an installation already holds, read
// the way the one it is being offered is read.
//
// A package installed before cpak migrated the legacy fields still carries them
// in its record. Comparing that record with a migrated override field by field
// reports every legacy field as a removal and every grant it stands for as an
// addition, so an update that changes nothing at all stops and asks the user to
// approve permissions the publisher never added.
//
// Reading the record is not rewriting it. It is rewritten by the update that
// weighed it, in the same step that enrols the anchor over it, because a record
// that stays behind the ledger describes a launch the ledger does not recognise.
func installedRecordOverride(app types.Application) types.Override {
	return app.ParsedOverride.WithMigratedFilesystem()
}

func migrateLegacyHostCommands(override *types.Override) error {
	if len(override.AllowedHostCommands) == 0 {
		return nil
	}
	for _, command := range override.AllowedHostCommands {
		switch command {
		case "notify-send":
			override.Notification = true
		case "xdg-open":
			override.OpenURI = true
		case "cpak-launch-app":
			override.HostApplications = true
		default:
			return fmt.Errorf("host command %q has no typed provider", command)
		}
	}
	override.AllowedHostCommands = nil
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
