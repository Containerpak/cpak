/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package types

import (
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

// CpakOptions is the struct that represents the options for the Cpak struct.
type CpakOptions struct {
	// BinPath is the path to the directory where the internal binaries
	// will be stored.
	BinPath string `json:"bin_path" conf:"bin_path"`

	// ManifestsPath is the path to the directory where the manifests
	// will be stored.
	//
	// Note: manifests stored in this directory are not meant to be
	// used, just stored for future use and debug purposes.
	ManifestsPath string `json:"manifests_path" conf:"manifests_path"`

	// ExportsPath is the path to the directory where the exports
	// (binaries and desktop entries) will be stored.
	ExportsPath string `json:"exports_path" conf:"exports_path"`

	// StorePath is the path to the directory where the images, containers,
	// states and the sqlite database will be stored.
	StorePath string `json:"store_path" conf:"store_path"`

	// CachePath is the path to the directory where the cache will be stored.
	//
	// Note: cache stores manifests and verified runtime sources. OCI layers
	// stream directly into the content store.
	CachePath string `json:"cache_path" conf:"cache_path"`

	// RegistryAuthPath stores public registry credential bindings.
	RegistryAuthPath string `json:"registry_auth_path" conf:"registry_auth_path"`

	// StorageDriver selects the runtime layer checkout driver.
	StorageDriver string `json:"storage_driver" conf:"storage_driver"`

	// DaBaDeeStoreopts is the configuration for the DaBaDee store.
	DaBaDeeStoreOptions store.Options `json:"dabadee_store"`

	// Following paths are not meant to be set by the user, they are set
	// by cpak during its initialization.
	StoreLayersPath     string `json:"store_layers_path"`
	StoreStatesPath     string `json:"store_states_path"`
	StoreContainersPath string `json:"store_containers_path"`
}
