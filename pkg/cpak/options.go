/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
)

// Options is how a Cpak is configured: where it keeps what it downloads, what
// it exports, and which driver checks out a layer.
//
// It lives here rather than in pkg/types because it is not part of the domain a
// manifest describes, it is how one installation of cpak is set up. Keeping it
// out of pkg/types is also what lets that package be built for a target with no
// filesystem, since the content store it names cannot be.
type Options struct {
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

	// OperatorNamedPaths holds the directories above whose location came from
	// cpak.json or a CPAK_ variable instead of cpak's own layout.
	//
	// cpak keeps its tree private to the user who owns it, and where it laid
	// that tree out itself it also fixes a directory it finds open. A path
	// somebody else named is a different thing: they chose it, something else
	// may be sharing it, and quietly narrowing it under them is the defect
	// this installation just removed from its service socket. Such a directory
	// is created private when it is missing and only reported on when it is
	// already there.
	//
	// getCpakOptions fills this. An Options built in code names nothing,
	// which is the right answer: those paths were chosen by cpak.
	OperatorNamedPaths map[string]bool `json:"-"`
}

// keepPrivate keeps one directory of this installation readable by its owner
// alone, under whichever of the two rules the path falls.
func (o Options) keepPrivate(path string) error {
	if o.OperatorNamedPaths[path] {
		return adoptPrivateDirectory(path)
	}
	return securePrivateDirectory(path)
}
