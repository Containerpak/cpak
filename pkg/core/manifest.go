/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

var (
	sessionIDPattern     = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)
	addonNamePattern     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	serviceNamePattern   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	addonEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// DecodeManifest parses a manifest without accepting unknown fields.
//
// It reads the bytes twice on purpose. The struct answers what the manifest
// says, and the raw map answers what it wrote down, which is a different
// question: a permission set to false and one nobody mentioned both arrive as
// false, and the difference decides whether a v1 field is present and whether a
// v2 filesystem list was declared at all.
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
		Sessions []struct {
			Override map[string]json.RawMessage `json:"override"`
		} `json:"sessions"`
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
	removed := []string{}
	for _, field := range manifestV3RemovedOverrideFields() {
		if _, ok := raw.Override[field]; ok {
			removed = append(removed, "override."+field)
		}
		for index, session := range raw.Sessions {
			if _, ok := session.Override[field]; ok {
				removed = append(removed, fmt.Sprintf("sessions[%d].override.%s", index, field))
			}
		}
	}
	manifest.SetManifestV3RemovedFields(removed)
	return manifest, nil
}

// ValidateManifest decides whether a manifest means anything, and reports the
// first thing that stops it from meaning what it says.
//
// It is the half of validation that is about cpak rather than about JSON: the
// runtime pairs it with the schema, which answers shapes and patterns, while
// this answers the rules a shape cannot carry. It also settles the manifest
// version and folds away the legacy host command list, so a caller that
// validates gets back a manifest the rest of the runtime can read.
func ValidateManifest(manifest *types.CpakManifest) error {
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	if manifest.ManifestVersion != "1.0" && manifest.ManifestVersion != "2.0" && manifest.ManifestVersion != "3.0" {
		return fmt.Errorf("unsupported manifest version: %s", manifest.ManifestVersion)
	}
	if manifest.ManifestVersion == "3.0" && len(manifest.ManifestV3RemovedFields()) > 0 {
		return fmt.Errorf("manifest version 3.0 contains removed fields: %s", strings.Join(manifest.ManifestV3RemovedFields(), ", "))
	}
	if manifest.Name == "" {
		return errors.New("name is mandatory and must be populated")
	}
	if manifest.Description == "" {
		return errors.New("description is mandatory and must be populated")
	}
	if err := validateManifestText(manifest); err != nil {
		return err
	}
	if manifest.Image == "" {
		return errors.New("image is mandatory and must be populated")
	}
	if _, err := ParseImageReference(manifest.Image); err != nil {
		return fmt.Errorf("image must be a valid OCI reference: %w", err)
	}
	if manifest.ImageRef != "" && manifest.ImageRef != "source" {
		return fmt.Errorf("unsupported image_ref: %s", manifest.ImageRef)
	}
	if manifest.ImageRef == "source" {
		ref, err := ParseImageReference(manifest.Image)
		if err != nil {
			return fmt.Errorf("image must be a valid OCI reference: %w", err)
		}
		if ref.IsDigest {
			return errors.New("image_ref source cannot be used with an image digest")
		}
	}
	if len(manifest.Binaries) == 0 {
		return errors.New("binaries is mandatory and must be populated")
	}
	if err := validateApplicationServices(manifest); err != nil {
		return err
	}
	for _, entry := range manifest.DesktopEntries {
		if err := validateDesktopEntryName(entry); err != nil {
			return err
		}
	}
	for _, dependency := range manifest.Dependencies {
		if dependency.Mode != "" && dependency.Mode != "nested" && dependency.Mode != "layer" {
			return fmt.Errorf("unsupported dependency mode for %s: %s", dependency.Origin, dependency.Mode)
		}
	}
	for _, source := range manifest.RuntimeSources {
		if err := ValidateRuntimeSource(source); err != nil {
			return err
		}
	}
	if err := validateAddonProvider(manifest.AddonProvider); err != nil {
		return err
	}
	if err := MigrateLegacyHostCommands(&manifest.Override); err != nil {
		return err
	}
	if err := types.ValidateHostActions(manifest.Override.HostActions); err != nil {
		return err
	}
	if err := types.ValidateFilePickerGrant(manifest.Override.FilePicker); err != nil {
		return err
	}
	if err := types.ValidateNetworkPermissions(manifest.Override); err != nil {
		return err
	}
	if err := validateManifestBusPolicy(manifest.ManifestVersion, "application", manifest.Override); err != nil {
		return err
	}
	if err := validateSessions(manifest); err != nil {
		return err
	}
	if manifest.ManifestVersion == "1.0" {
		if len(manifest.Override.HostActions) > 0 {
			return errors.New("host actions require manifest version 2.0")
		}
		if manifest.FilesystemDeclared() || len(manifest.Override.Filesystem) > 0 {
			return errors.New("filesystem permissions require manifest version 2.0")
		}
	} else {
		if fields := LegacyFilesystemFields(manifest); len(fields) > 0 {
			return fmt.Errorf("legacy filesystem permissions are not supported in manifest version %s: %s", manifest.ManifestVersion, strings.Join(fields, ", "))
		}
		if err := types.ValidateFilesystemPermissions(manifest.Override.Filesystem); err != nil {
			return err
		}
	}
	if manifest.ManifestVersion == "3.0" {
		if manifest.ImageRef != "" {
			return errors.New("manifest version 3.0 requires a digest-pinned image")
		}
		reference, err := ParseImageReference(manifest.Image)
		if err != nil || !reference.IsDigest {
			return errors.New("manifest version 3.0 requires a digest-pinned image")
		}
	}
	return ValidateManifestSchema(manifest)
}

func validateApplicationServices(manifest *types.CpakManifest) error {
	binaries := make(map[string]bool, len(manifest.Binaries))
	for _, binary := range manifest.Binaries {
		binaries[binary] = true
	}
	for name, service := range manifest.Services {
		if !serviceNamePattern.MatchString(name) || len(name) > 64 {
			return fmt.Errorf("invalid service name: %q", name)
		}
		if !binaries[service.Binary] {
			return fmt.Errorf("service %s binary is not exported: %s", name, service.Binary)
		}
		for _, argument := range service.Arguments {
			if err := validateManifestLine(argument); err != nil {
				return fmt.Errorf("service %s has an invalid argument", name)
			}
		}
	}
	return nil
}

func validateManifestBusPolicy(version, scope string, override types.Override) error {
	if (override.DisplayX11 || override.Bluetooth) && version != "3.0" {
		return fmt.Errorf("%s isolated desktop capabilities require manifest version 3.0", scope)
	}
	for _, permission := range []struct {
		enabled bool
		name    string
	}{
		{override.SocketSessionBus, "socketSessionBus"},
		{override.SocketSystemBus, "socketSystemBus"},
		{override.SocketX11, "socketX11"},
		{override.SocketAtSpiBus, "socketAtSpiBus"},
		{override.SocketBluetooth, "socketBluetooth"},
	} {
		if permission.enabled {
			return fmt.Errorf("%s cannot grant raw host access through %s", scope, permission.name)
		}
	}
	if err := types.ValidateDBusPolicy(override.SessionBus); err != nil {
		return fmt.Errorf("%s session bus policy: %w", scope, err)
	}
	if override.SessionBus.Enabled() && version != "3.0" {
		return fmt.Errorf("%s session bus policy requires manifest version 3.0", scope)
	}
	for _, rule := range override.SessionBus.Talk {
		if forbiddenSessionBusName(rule.Name) {
			return fmt.Errorf("%s session bus policy cannot call %s", scope, rule.Name)
		}
	}
	for _, name := range override.SessionBus.Own {
		if forbiddenSessionBusName(name) {
			return fmt.Errorf("%s session bus policy cannot own %s", scope, name)
		}
	}
	return nil
}

func forbiddenSessionBusName(name string) bool {
	switch name {
	case "org.freedesktop.DBus",
		"org.freedesktop.Flatpak",
		"org.freedesktop.portal.Desktop",
		"org.freedesktop.secrets",
		"org.freedesktop.systemd1",
		"org.gnome.keyring":
		return true
	default:
		return false
	}
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
		if err := types.ValidateNetworkPermissions(session.Override); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
		if err := validateManifestBusPolicy(manifest.ManifestVersion, "session "+session.ID, session.Override); err != nil {
			return err
		}
		if err := types.ValidateFilesystemPermissions(session.Override.Filesystem); err != nil {
			return fmt.Errorf("session %s: %w", session.ID, err)
		}
	}
	return nil
}

func validateSessionText(value string, limit int) error {
	if value == "" || len(value) > limit || carriesTerminalControl(value) {
		return errors.New("invalid session text")
	}
	return nil
}

func validateDesktopEntryName(entry string) error {
	name := path.Base(entry)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return fmt.Errorf("invalid desktop entry: %q", entry)
	}
	if !strings.HasSuffix(name, ".desktop") || name == ".desktop" {
		return fmt.Errorf("desktop entry %q is not a .desktop file", entry)
	}
	return nil
}

func validateAddonProvider(provider *types.AddonProvider) error {
	if provider == nil {
		return nil
	}
	if !addonNamePattern.MatchString(provider.ID) {
		return fmt.Errorf("invalid addon provider id: %q", provider.ID)
	}
	if !addonNamePattern.MatchString(provider.Slot) {
		return fmt.Errorf("invalid addon slot: %q", provider.Slot)
	}
	if provider.Mode != types.AddonSlotExclusive && provider.Mode != types.AddonSlotMultiple {
		return fmt.Errorf("unsupported addon slot mode for %s: %s", provider.Slot, provider.Mode)
	}
	paths := [][]string{
		provider.Exports.Path,
		provider.Exports.LibraryPath,
		provider.Exports.IncludePath,
		provider.Exports.PkgConfigPath,
		provider.Exports.CMakePrefixPath,
	}
	for _, entries := range paths {
		for _, entry := range entries {
			if !path.IsAbs(entry) || path.Clean(entry) != entry || strings.Contains(entry, ":") {
				return fmt.Errorf("invalid addon export path: %q", entry)
			}
		}
	}
	for _, variable := range provider.Exports.Environment {
		name, _, found := strings.Cut(variable, "=")
		if !found || !addonEnvironmentName.MatchString(name) {
			return fmt.Errorf("invalid addon environment entry: %q", variable)
		}
	}
	return nil
}

const manifestTextLimit = 4096

func carriesTerminalControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return true
		}
	}
	return false
}

func validateManifestText(manifest *types.CpakManifest) error {
	if err := validateManifestProse(manifest.Description); err != nil {
		return errors.New("description contains a control character or is too long")
	}
	if err := validateManifestLine(manifest.Name); err != nil {
		return errors.New("name contains a control character or is too long")
	}
	for _, binary := range manifest.Binaries {
		if err := validateManifestLine(binary); err != nil {
			return errors.New("a binary path contains a control character or is too long")
		}
	}
	for _, service := range manifest.Services {
		if err := validateManifestLine(service.Binary); err != nil {
			return errors.New("a service binary contains a control character or is too long")
		}
		for _, argument := range service.Arguments {
			if err := validateManifestLine(argument); err != nil {
				return errors.New("a service argument contains a control character or is too long")
			}
		}
	}
	for _, entry := range manifest.DesktopEntries {
		if err := validateManifestLine(entry); err != nil {
			return errors.New("a desktop entry contains a control character or is too long")
		}
	}
	for _, dependency := range manifest.Dependencies {
		for _, value := range []string{dependency.Id, dependency.Origin, dependency.Branch, dependency.Release, dependency.Commit, dependency.Mode} {
			if err := validateManifestLine(value); err != nil {
				return errors.New("a dependency contains a control character or is too long")
			}
		}
	}
	for _, source := range manifest.RuntimeSources {
		for _, value := range []string{source.Name, source.URL, source.SHA256, source.Installer, source.Architecture} {
			if err := validateManifestLine(value); err != nil {
				return errors.New("a runtime source contains a control character or is too long")
			}
		}
	}
	for _, addon := range manifest.Addons {
		if err := validateManifestLine(addon); err != nil {
			return errors.New("an addon contains a control character or is too long")
		}
	}
	if provider := manifest.AddonProvider; provider != nil {
		values := []string{provider.ID, provider.Slot, provider.Mode}
		values = append(values, provider.Exports.Path...)
		values = append(values, provider.Exports.LibraryPath...)
		values = append(values, provider.Exports.IncludePath...)
		values = append(values, provider.Exports.PkgConfigPath...)
		values = append(values, provider.Exports.CMakePrefixPath...)
		values = append(values, provider.Exports.Environment...)
		for _, value := range values {
			if err := validateManifestLine(value); err != nil {
				return errors.New("an addon provider contains a control character or is too long")
			}
		}
	}
	if err := validateOverrideText(manifest.Override); err != nil {
		return err
	}
	for _, session := range manifest.Sessions {
		if err := validateOverrideText(session.Override); err != nil {
			return err
		}
	}
	return nil
}

func validateOverrideText(override types.Override) error {
	for _, variable := range override.Env {
		if err := validateManifestLine(variable); err != nil {
			return errors.New("an environment variable contains a control character or is too long")
		}
	}
	for _, permission := range override.Filesystem {
		if err := validateManifestLine(permission.Path); err != nil {
			return errors.New("a filesystem path contains a control character or is too long")
		}
		if err := validateManifestLine(permission.Access); err != nil {
			return errors.New("a filesystem access mode contains a control character or is too long")
		}
	}
	for _, path := range override.FsExtra {
		if err := validateManifestLine(path); err != nil {
			return errors.New("a filesystem path contains a control character or is too long")
		}
	}
	for _, command := range override.AllowedHostCommands {
		if err := validateManifestLine(command); err != nil {
			return errors.New("a host command contains a control character or is too long")
		}
	}
	for _, action := range override.HostActions {
		if err := validateManifestLine(action.Provider); err != nil {
			return errors.New("a host action provider contains a control character or is too long")
		}
		for _, capability := range action.Capabilities {
			if err := validateManifestLine(capability); err != nil {
				return errors.New("a host action capability contains a control character or is too long")
			}
		}
	}
	return nil
}

func validateManifestLine(value string) error {
	if len(value) > manifestTextLimit || carriesTerminalControl(value) {
		return errors.New("invalid manifest text")
	}
	return nil
}

func validateManifestProse(value string) error {
	if len(value) > manifestTextLimit {
		return errors.New("invalid manifest text")
	}
	for _, character := range value {
		if character == '\n' || character == '\t' {
			continue
		}
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return errors.New("invalid manifest text")
		}
	}
	return nil
}

// MigrateManifest upgrades a v1 manifest to v2 while preserving its effective
// permissions and replacing legacy filesystem fields with typed grants.
func MigrateManifest(manifest *types.CpakManifest) error {
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	if manifest.ManifestVersion == "2.0" || manifest.ManifestVersion == "3.0" {
		return MigrateLegacyHostCommands(&manifest.Override)
	}
	if manifest.ManifestVersion != "1.0" {
		return fmt.Errorf("unsupported manifest version: %s", manifest.ManifestVersion)
	}
	override := manifest.Override.WithMigratedFilesystem()
	if err := types.ValidateFilesystemPermissions(override.Filesystem); err != nil {
		return fmt.Errorf("migrate filesystem permissions: %w", err)
	}
	if err := MigrateLegacyHostCommands(&override); err != nil {
		return err
	}
	manifest.ManifestVersion = "2.0"
	manifest.Override = override
	manifest.SetLegacyFilesystemFields(nil)
	manifest.SetFilesystemDeclared(len(override.Filesystem) > 0)
	return nil
}

func manifestV3RemovedOverrideFields() []string {
	return []string{
		"fsHost", "fsHostEtc", "fsHostHome", "fsExtra",
		"socketX11", "socketSessionBus", "socketSystemBus", "socketAtSpiBus", "socketBluetooth",
	}
}

func manifestV3RemovedPermission(key string) bool {
	for _, removed := range manifestV3RemovedOverrideFields() {
		if key == removed {
			return true
		}
	}
	return false
}

// MigrateLegacyHostCommands turns the v1 list of host programs an application
// was allowed to run into the typed permissions that replaced it. A command
// with no typed provider is refused rather than dropped, because dropping it
// would be a silent narrowing of what the author asked for.
func MigrateLegacyHostCommands(override *types.Override) error {
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

// LegacyFilesystemFields names the v1 filesystem fields a manifest still
// carries, whether it wrote them or merely means them.
func LegacyFilesystemFields(manifest *types.CpakManifest) []string {
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
