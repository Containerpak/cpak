/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

// CpakManifest is the struct that represents the manifest of an application.
type CpakManifest struct {
	// Name is the name of the application.
	Name string `json:"name" jsonschema:"minLength=1,description=Application name"`

	// Description is the description of the application. It is expected to be
	// as concise as possible.
	Description string `json:"description" jsonschema:"minLength=1,description=Short application description"`

	// Version is the version of the application.
	Version string `json:"version" jsonschema:"pattern=^v?[0-9]+(\\.[0-9]+)*(?:[-+][0-9A-Za-z.-]+)?$,description=Semver-like version"`

	// Image is the image of the application. It is expected to be a valid
	// OCI image (full image reference).
	Image string `json:"image" jsonschema:"pattern=^[a-z0-9]+(?:[._-][a-z0-9]+)*/[A-Za-z0-9._-]+(?::[A-Za-z0-9._-]+)?$,description=OCI image reference"`

	// Binaries is the list of exported binaries of the application.
	Binaries []string `json:"binaries" jsonschema:"minItems=1,items.pattern=^/,description=Absolute paths to binaries"`

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries []string `json:"desktop_entries" jsonschema:"items.pattern=.+\\.desktop$,description=.desktop entry files"`

	// Dependencies is the list of dependencies of the application, it is
	// expected to be a list of origin repositories.
	//
	// Note: versions are not supported yet.
	Dependencies []Dependency `json:"dependencies,omitempty" jsonschema:"description=cpak dependencies"`

	// Addons is the list of additional applications which it supports.
	Addons []string `json:"addons,omitempty" jsonschema:"description=Optional addons"`

	// IdleTime is the idle time in minutes, after which to destroy the
	// container.
	//
	// FIXME: implement IdleTime field usage.
	IdleTime int `json:"idle_time" jsonschema:"minimum=0,description=Idle time in minutes before stop"`

	// Override is a set of permissions that the user can grant to the
	// application, even if this is called "override", it is also used to
	// set the default permissions.
	Override Override `json:"override" jsonschema:"description=Permissions override settings"`
}
