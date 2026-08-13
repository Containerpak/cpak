/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package types

// OciManifest is the struct that represents the manifest of an OCI image.
type OciManifest struct {

	// Config is the path to the config file of the image.
	Config string `json:"Config"`

	// RepoTags is the list of tags of the image.
	RepoTags []string `json:"RepoTags"`

	// Layers is the list of layers of the image.
	Layers []string `json:"Layers"`
}
