/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

import (
	"github.com/mirkobrombin/dabadee/pkg/storage"
)

// CpakOptions is the struct that represents the options for the Cpak struct.
type CpakOptions struct {
	// BinPath is the path to the directory where the internal binaries
	// will be stored.
	BinPath string `json:"bin_path"`

	// ManifestsPath is the path to the directory where the manifests
	// will be stored.
	//
	// Note: manifests stored in this directory are not meant to be
	// used, just stored for future use and debug purposes.
	ManifestsPath string `json:"manifests_path"`

	// ExportsPath is the path to the directory where the exports
	// (binaries and desktop entries) will be stored.
	ExportsPath string `json:"exports_path"`

	// StorePath is the path to the directory where the images, containers,
	// states and the sqlite database will be stored.
	StorePath string `json:"store_path"`

	// CachePath is the path to the directory where the cache will be stored.
	//
	// Note: cache is intended to be used by the cpak pull function to store
	// the downloaded images and unpacked layers.
	CachePath string `json:"cache_path"`

	// DaBaDeeStoreopts is the configuration for the DaBaDee store.
	DaBaDeeStoreOptions storage.StorageOptions `json:"dabadee_store"`

	// Following paths are not meant to be set by the user, they are set
	// by cpak during its initialization.
	StoreLayersPath     string `json:"store_layers_path"`
	StoreStatesPath     string `json:"store_states_path"`
	StoreContainersPath string `json:"store_containers_path"`
	RotlesskitBinPath   string `json:"rootlesskit_bin_path"`
	NsenterBinPath      string `json:"nsenter_bin_path"`
}
