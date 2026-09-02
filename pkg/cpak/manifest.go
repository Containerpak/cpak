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
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/types"
)

var (
	sessionIDPattern     = regexp.MustCompile(`^[a-z0-9]+(?:[.-][a-z0-9]+)*$`)
	addonNamePattern     = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	serviceNamePattern   = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	addonEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidateManifest validates a manifest file, by ensuring all
// required fields are present.
func (c *Cpak) ValidateManifest(manifest *types.CpakManifest) (err error) {
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
	// Before anything else reports on this manifest: several of the checks
	// below echo the value they rejected back to the terminal with %s, and
	// everything downstream that writes a manifest string somewhere assumes
	// the string is inert.
	if err = validateManifestText(manifest); err != nil {
		return err
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
	if err = validateApplicationServices(manifest); err != nil {
		return err
	}
	for _, entry := range manifest.DesktopEntries {
		if _, err = desktopEntryExportName(entry); err != nil {
			return err
		}
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
	if err = validateAddonProvider(manifest.AddonProvider); err != nil {
		return err
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
	if err = types.ValidateNetworkPermissions(manifest.Override); err != nil {
		return err
	}
	if err = validateManifestBusPolicy(manifest.ManifestVersion, "application", manifest.Override); err != nil {
		return err
	}
	if err = validateSessions(manifest); err != nil {
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
		if fields := legacyFilesystemFields(manifest); len(fields) > 0 {
			return fmt.Errorf("legacy filesystem permissions are not supported in manifest version %s: %s", manifest.ManifestVersion, strings.Join(fields, ", "))
		}
		if err = types.ValidateFilesystemPermissions(manifest.Override.Filesystem); err != nil {
			return err
		}
	}
	if manifest.ManifestVersion == "3.0" {
		if manifest.ImageRef != "" {
			return errors.New("manifest version 3.0 requires a digest-pinned image")
		}
		reference, parseErr := oci.ParseReference(manifest.Image)
		if parseErr != nil || !reference.IsDigest {
			return errors.New("manifest version 3.0 requires a digest-pinned image")
		}
	}
	return ValidateManifest(manifest)
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
	if err := types.ValidateClipboardGrant(override.Clipboard, override.DisplayX11); err != nil {
		return fmt.Errorf("%s clipboard policy: %w", scope, err)
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

func forbiddenSessionBusName(rule string) bool {
	for _, name := range []string{
		"org.freedesktop.DBus",
		"org.freedesktop.Flatpak",
		"org.freedesktop.portal.Desktop",
		"org.freedesktop.secrets",
		"org.freedesktop.systemd1",
		"org.gnome.keyring",
	} {
		if (types.DBusPolicy{Own: []string{rule}}).AllowsOwn(name) {
			return true
		}
	}
	return false
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

// manifestTextLimit bounds what one string of a manifest can put on a
// terminal. It is the same for a name, a path, a URL and the description: the
// description is the only one of them that is prose, and 4096 characters of
// prose is already more than any published package carries.
const manifestTextLimit = 4096

// carriesTerminalControl reports whether a string holds a character that drives
// the terminal rather than printing on it.
//
// The set is C0, DEL and C1, U+0080 to U+009F. C1 is the half that is easy to
// forget: U+009B is CSI, the single-character form of ESC [, and VTE (so GNOME
// Terminal, Console, Tilix and every emulator built on it) and xterm act on it
// in UTF-8 mode exactly as they act on the two-character form. It also survives
// a JSON manifest as a plain U+009B, which encoding/json accepts where it
// rejects a raw ESC byte, so a rule that stops at 0x7f leaves the whole attack
// reachable through a second spelling of the same control.
//
// tools.SanitizeForDisplay escapes exactly this set on the way to a terminal.
// The two are held together by TestTheValidatorRefusesWhatThePrinterEscapes,
// because a validator and a printer that disagree about what a control
// character is are how the C1 half was missed in the first place.
func carriesTerminalControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) {
			return true
		}
	}
	return false
}

// validateManifestText holds every publisher-controlled string in a manifest
// to the rule a session name already followed: nothing that drives a terminal.
//
// It is the second half of the defence, not the first. The install prompt
// escapes what it writes, because it writes this manifest before this function
// has ever run; this refuses to install a manifest that carried the sequences
// at all, so no later code (an error message, a desktop entry, a log line) has
// to remember to escape them again.
func validateManifestText(manifest *types.CpakManifest) error {
	// The description is the one string in a manifest that is prose, and a
	// package with a two-paragraph description is completely ordinary. A
	// newline and a tab are allowed here and nowhere else: neither moves the
	// cursor back over a line that is already written. Length is answered with
	// a limit rather than with a refusal of the characters prose is made of.
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
	for name, service := range manifest.Services {
		if err := validateManifestLine(name); err != nil {
			return errors.New("a service name contains a control character or is too long")
		}
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
	for _, factor := range manifest.FormFactors {
		if err := validateManifestLine(factor); err != nil {
			return errors.New("a form factor contains a control character or is too long")
		}
	}
	// The prompt prints a dependency as the whole struct, so every string in
	// it reaches the terminal, not only the origin.
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
	// A session override is printed under its own heading in the same prompt,
	// and the session's own name and kind are on the line above it.
	for _, session := range manifest.Sessions {
		if err := validateOverrideText(session.Override); err != nil {
			return err
		}
	}
	return nil
}

// validateOverrideText covers the strings a publisher writes inside a grant.
// They are printed under the permission heading, which is the last thing the
// reader looks at before answering.
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

// validateManifestLine holds a value that belongs on one line to one line.
func validateManifestLine(value string) error {
	if len(value) > manifestTextLimit || carriesTerminalControl(value) {
		return errors.New("invalid manifest text")
	}
	return nil
}

// validateManifestProse allows the two whitespace characters a paragraph is
// written with and refuses the rest, C1 included.
//
// This is the rule that decides whether an ordinary published package can be
// installed at all: ValidateManifest runs on install, update, lock and verify
// alike, so refusing a description for holding a newline does not merely turn
// away a new package, it makes an installed one impossible to update.
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

// MarshalManifest encodes a validated manifest in its published schema shape.
func MarshalManifest(manifest *types.CpakManifest) ([]byte, error) {
	if err := (&Cpak{}).ValidateManifest(manifest); err != nil {
		return nil, err
	}
	var removed []string
	switch manifest.ManifestVersion {
	case "2.0":
		removed = manifestV2RemovedOverrideFields()
	case "3.0":
		removed = manifestV3RemovedOverrideFields()
	default:
		return json.MarshalIndent(manifest, "", "  ")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	if raw, ok := document["override"]; ok {
		document["override"], err = marshalManifestOverride(raw, removed)
		if err != nil {
			return nil, err
		}
	}
	if raw, ok := document["sessions"]; ok {
		var sessions []map[string]json.RawMessage
		if err = json.Unmarshal(raw, &sessions); err != nil {
			return nil, err
		}
		for index := range sessions {
			override, ok := sessions[index]["override"]
			if !ok {
				continue
			}
			sessions[index]["override"], err = marshalManifestOverride(override, removed)
			if err != nil {
				return nil, err
			}
		}
		document["sessions"], err = json.Marshal(sessions)
		if err != nil {
			return nil, err
		}
	}
	return json.MarshalIndent(document, "", "  ")
}

func marshalManifestOverride(encoded json.RawMessage, removed []string) (json.RawMessage, error) {
	var override map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &override); err != nil {
		return nil, err
	}
	for _, field := range removed {
		delete(override, field)
	}
	return json.Marshal(override)
}

// MigrateManifest upgrades a v1 manifest to v2 while preserving its effective
// permissions and replacing legacy filesystem fields with typed grants.
func MigrateManifest(manifest *types.CpakManifest) error {
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "1.0"
	}
	if manifest.ManifestVersion == "2.0" || manifest.ManifestVersion == "3.0" {
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
	// A v1 fsExtra entry that names nothing cpak can hold an application to is
	// dropped rather than refused. Refusing is not a stricter reading of the
	// manifest, it is an installation that cannot be made and, through
	// installedOverride, an installed application that can never be updated
	// again, security updates included. The publisher is told which grant went,
	// and the application runs with less than it asked for, which is the side a
	// migration is allowed to err on.
	for _, path := range manifest.Override.FsExtra {
		if _, ok := types.LegacyFilesystemGrant(path); !ok {
			logger.Printf("Warning: %s asks for the path %q, which cpak cannot express as a grant; the application runs without it", manifest.Name, path)
		}
	}
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

	repoProvider, err := c.newRepoProvider(origin)
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
