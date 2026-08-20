/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

const ManifestSchemaURL = "https://raw.githubusercontent.com/Containerpak/cpak/v2/schema/manifest-v2.json"

// CpakManifest is the struct that represents the manifest of an application.
type CpakManifest struct {
	// Schema enables completion and validation in JSON editors.
	Schema string `json:"$schema,omitempty" jsonschema:"format=uri,description=JSON Schema used by editors"`

	// ManifestVersion is the version of the manifest schema (e.g. "1.0").
	ManifestVersion string `json:"manifest_version" jsonschema:"enum=1.0,enum=2.0,description=Manifest schema version"`

	// Name is the name of the application.
	Name string `json:"name" jsonschema:"minLength=1,description=Application name"`

	// Description is the description of the application. It is expected to be
	// as concise as possible.
	Description string `json:"description" jsonschema:"minLength=1,description=Short application description"`

	// Version is the version of the application.
	Version string `json:"version,omitempty" jsonschema:"description=Application version"`

	// Image is the image of the application. It is expected to be a valid
	// OCI image (full image reference).
	Image string `json:"image" jsonschema:"minLength=1,description=OCI image reference or digest"`

	// ImageRef selects an OCI tag derived from the requested Git source.
	ImageRef string `json:"image_ref,omitempty" jsonschema:"enum=source,description=Track the selected Git branch release or commit"`

	// Binaries is the list of exported binaries of the application.
	Binaries []string `json:"binaries" jsonschema:"minItems=1,description=Absolute paths to binaries"`

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries []string `json:"desktop_entries,omitempty" jsonschema:"description=.desktop entry files"`

	// Sessions are login sessions which can be registered with a display manager.
	Sessions []Session `json:"sessions,omitempty" jsonschema:"description=Desktop and kiosk login sessions"`

	// Dependencies is the list of dependencies of the application, it is
	// expected to be a list of origin repositories.
	//
	// Note: versions are not supported yet.
	Dependencies []Dependency `json:"dependencies,omitempty" jsonschema:"description=cpak dependencies"`

	// Addons is the list of additional applications which it supports.
	Addons []string `json:"addons,omitempty" jsonschema:"description=Optional addons"`

	// AddonProvider describes the slot supplied when this package is used as an addon.
	AddonProvider *AddonProvider `json:"addon_provider,omitempty" jsonschema:"description=Addon capability supplied by this package"`

	// IdleTime is the idle time in minutes, after which to stop the container.
	IdleTime int `json:"idle_time" jsonschema:"minimum=0,description=Idle time in minutes before stop"`

	// Override is a set of permissions that the user can grant to the
	// application, even if this is called "override", it is also used to
	// set the default permissions.
	Override Override `json:"override" jsonschema:"description=Permissions override settings"`

	RuntimeSources []RuntimeSource `json:"runtime_sources,omitempty" jsonschema:"description=External artifacts fetched at install time"`

	legacyFilesystemFields []string
	filesystemDeclared     bool
}

func (m *CpakManifest) SetLegacyFilesystemFields(fields []string) {
	m.legacyFilesystemFields = append([]string{}, fields...)
}

func (m CpakManifest) LegacyFilesystemFields() []string {
	return append([]string{}, m.legacyFilesystemFields...)
}

func (m *CpakManifest) SetFilesystemDeclared(declared bool) {
	m.filesystemDeclared = declared
}

func (m CpakManifest) FilesystemDeclared() bool {
	return m.filesystemDeclared
}

type RuntimeSource struct {
	Name      string `json:"name,omitempty" jsonschema:"description=File name of the artifact"`
	URL       string `json:"url" jsonschema:"pattern=^https://,description=HTTPS URL of the artifact"`
	SHA256    string `json:"sha256" jsonschema:"pattern=^[A-Fa-f0-9]{64}$,description=SHA256 checksum of the artifact"`
	Size      int64  `json:"size" jsonschema:"minimum=1,description=Size of the artifact in bytes"`
	Installer string `json:"installer" jsonschema:"enum=dpkg,enum=rpm,enum=tar,description=Installer used inside the cpak environment"`
}

type Session struct {
	ID          string   `json:"id" jsonschema:"pattern=^[a-z0-9]+(?:[.-][a-z0-9]+)*$,maxLength=96,description=Globally unique session identifier"`
	Name        string   `json:"name" jsonschema:"minLength=1,maxLength=80,description=Session name shown by the display manager"`
	Description string   `json:"description" jsonschema:"maxLength=160,description=Short session description"`
	Kind        string   `json:"kind" jsonschema:"enum=desktop,enum=kiosk,description=Login session type"`
	Entrypoint  string   `json:"entrypoint" jsonschema:"pattern=^/,description=Exported binary used to start the session"`
	Override    Override `json:"override" jsonschema:"description=Permissions used by the full login session"`
}
