/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

const (
	AddonSlotExclusive = "exclusive"
	AddonSlotMultiple  = "multiple"
)

// AddonProvider describes how an addon joins an application at runtime.
type AddonProvider struct {
	ID      string       `json:"id" jsonschema:"pattern=^[a-z0-9]+(?:[._-][a-z0-9]+)*$,description=Provider identifier within its slot"`
	Slot    string       `json:"slot" jsonschema:"pattern=^[a-z0-9]+(?:[._-][a-z0-9]+)*$,description=Capability supplied by this package"`
	Mode    string       `json:"mode" jsonschema:"enum=exclusive,enum=multiple,description=Whether one or several providers may be active"`
	Exports AddonExports `json:"exports,omitempty" jsonschema:"description=Runtime paths and environment supplied by the provider"`
}

// AddonExports makes non-standard tool locations available to consumers.
type AddonExports struct {
	Path            []string `json:"path,omitempty" jsonschema:"description=Directories prepended to PATH"`
	LibraryPath     []string `json:"library_path,omitempty" jsonschema:"description=Directories prepended to runtime and compiler library paths"`
	IncludePath     []string `json:"include_path,omitempty" jsonschema:"description=Directories prepended to CPATH"`
	PkgConfigPath   []string `json:"pkg_config_path,omitempty" jsonschema:"description=Directories prepended to PKG_CONFIG_PATH"`
	CMakePrefixPath []string `json:"cmake_prefix_path,omitempty" jsonschema:"description=Directories prepended to CMAKE_PREFIX_PATH"`
	Environment     []string `json:"environment,omitempty" jsonschema:"description=Environment entries in NAME=value form"`
}
