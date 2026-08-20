/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func applyAddonEnvironment(environment []string, addons []types.Application) []string {
	paths := make(map[string][]string)
	for _, addon := range addons {
		provider := addon.ParsedAddonProvider
		if provider == nil {
			continue
		}
		paths["PATH"] = append(paths["PATH"], provider.Exports.Path...)
		paths["LD_LIBRARY_PATH"] = append(paths["LD_LIBRARY_PATH"], provider.Exports.LibraryPath...)
		paths["LIBRARY_PATH"] = append(paths["LIBRARY_PATH"], provider.Exports.LibraryPath...)
		paths["CPATH"] = append(paths["CPATH"], provider.Exports.IncludePath...)
		paths["PKG_CONFIG_PATH"] = append(paths["PKG_CONFIG_PATH"], provider.Exports.PkgConfigPath...)
		paths["CMAKE_PREFIX_PATH"] = append(paths["CMAKE_PREFIX_PATH"], provider.Exports.CMakePrefixPath...)
	}
	for _, name := range []string{"PATH", "LD_LIBRARY_PATH", "LIBRARY_PATH", "CPATH", "PKG_CONFIG_PATH", "CMAKE_PREFIX_PATH"} {
		if len(paths[name]) > 0 {
			environment = prependEnvironmentPaths(environment, name, paths[name])
		}
	}
	for _, addon := range addons {
		provider := addon.ParsedAddonProvider
		if provider == nil {
			continue
		}
		for _, variable := range provider.Exports.Environment {
			name, value, found := strings.Cut(variable, "=")
			if found {
				environment = setEnvironmentValue(environment, name, value)
			}
		}
	}
	return environment
}

func prependEnvironmentPaths(environment []string, name string, entries []string) []string {
	all := append([]string{}, entries...)
	if current := environmentValue(environment, name); current != "" {
		all = append(all, strings.Split(current, ":")...)
	}
	seen := make(map[string]bool, len(all))
	paths := make([]string, 0, len(all))
	for _, entry := range all {
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		paths = append(paths, entry)
	}
	return setEnvironmentValue(environment, name, strings.Join(paths, ":"))
}
