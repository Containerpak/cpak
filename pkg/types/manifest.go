/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

// CpakManifest is the struct that represents the manifest of an application.
type CpakManifest struct {
	// Name is the name of the application.
	Name string `json:"name"`

	// Description is the description of the application. It is expected to be
	// as concise as possible.
	Description string `json:"description"`

	// Version is the version of the application.
	Version string `json:"version"`

	// Image is the image of the application. It is expected to be a valid
	// OCI image (full image reference).
	Image string `json:"image"`

	// Binaries is the list of exported binaries of the application.
	Binaries []string `json:"binaries"`

	// DesktopEntries is the list of exported desktop entries of the application.
	DesktopEntries []string `json:"desktop_entries"`

	// Dependencies is the list of dependencies of the application, it is
	// expected to be a list of origin repositories.
	//
	// Note: versions are not supported yet.
	Dependencies []Dependency `json:"dependencies"`

	// Addons is the list of additional applications which it supports.
	Addons []string `json:"addons"`

	// IdleTime is the idle time in minutes, after which to destroy the
	// container.
	//
	// Note: this is not used yet.
	IdleTime int `json:"idle_time"` // non mandatory, idle time in minutes, after which to destroy the container

	// Override is a set of permissions that the user can grant to the
	// application, even if this is called "override", it is also used to
	// set the default permissions.
	Override Override `json:"override"`
}
