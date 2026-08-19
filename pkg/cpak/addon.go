/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const addonConfigurationVersion = 1

type addonConfiguration struct {
	Version int      `json:"version"`
	Enabled []string `json:"enabled"`
}

type AddonStatus struct {
	Origin    string `json:"origin"`
	Enabled   bool   `json:"enabled"`
	Installed bool   `json:"installed"`
}

type addonPolicyIdentity struct {
	CpakID      string   `json:"cpak_id"`
	ImageDigest string   `json:"image_digest"`
	Layers      []string `json:"layers"`
}

func addonConfigurationPath(app types.Application) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	localName, err := getCpakLocalName(app.Origin)
	if err != nil {
		return "", err
	}
	version := url.PathEscape(addonConfigurationKey(app))
	if version == "" {
		version = "default"
	}
	return filepath.Join(home, ".config", "cpak", "addons", localName, version, "addons.json"), nil
}

func addonConfigurationKey(app types.Application) string {
	switch {
	case app.Branch != "":
		return "branch-" + app.Branch
	case app.Release != "":
		return "release"
	case app.Commit != "":
		return "commit-" + app.Commit
	default:
		return app.Version
	}
}

func loadEnabledAddons(app types.Application) ([]string, error) {
	path, err := addonConfigurationPath(app)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var configuration addonConfiguration
	if err := decoder.Decode(&configuration); err != nil {
		return nil, fmt.Errorf("decode addon configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode addon configuration: trailing data")
	}
	if configuration.Version != addonConfigurationVersion {
		return nil, fmt.Errorf("unsupported addon configuration version %d", configuration.Version)
	}
	return normalizedOrigins(configuration.Enabled), nil
}

func saveEnabledAddons(app types.Application, enabled []string) error {
	path, err := addonConfigurationPath(app)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(addonConfiguration{
		Version: addonConfigurationVersion,
		Enabled: normalizedOrigins(enabled),
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(path), ".addons.partial-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func normalizedOrigins(origins []string) []string {
	seen := make(map[string]bool, len(origins))
	result := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.ToLower(strings.TrimSpace(origin))
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		result = append(result, origin)
	}
	sort.Strings(result)
	return result
}

func supportedAddon(app types.Application, origin string) bool {
	origin = strings.ToLower(origin)
	for _, supported := range app.ParsedAddons {
		if strings.ToLower(supported) == origin {
			return true
		}
	}
	return false
}

func (c *Cpak) AddonStatuses(app types.Application) ([]AddonStatus, error) {
	enabled, err := loadEnabledAddons(app)
	if err != nil {
		return nil, err
	}
	enabledSet := make(map[string]bool, len(enabled))
	for _, origin := range enabled {
		enabledSet[origin] = true
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()

	statuses := make([]AddonStatus, 0, len(app.ParsedAddons))
	for _, origin := range normalizedOrigins(app.ParsedAddons) {
		installed, getErr := store.GetApplicationByOrigin(origin, "", "", "", "")
		statuses = append(statuses, AddonStatus{
			Origin:    origin,
			Enabled:   enabledSet[origin],
			Installed: getErr == nil && installed.CpakId != "",
		})
	}
	return statuses, nil
}

func (c *Cpak) EnableAddon(app types.Application, origin string) error {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if !supportedAddon(app, origin) {
		return fmt.Errorf("addon %s is not supported by %s", origin, app.Name)
	}
	if err := c.Install(origin, "main", "", ""); err != nil {
		return fmt.Errorf("install addon %s: %w", origin, err)
	}
	enabled, err := loadEnabledAddons(app)
	if err != nil {
		return err
	}
	for _, current := range enabled {
		if current == origin {
			return nil
		}
	}
	if err := saveEnabledAddons(app, append(enabled, origin)); err != nil {
		return err
	}
	if err := c.PrepareApplicationStorage(app); err != nil {
		return err
	}
	return c.StopContainer(app)
}

func (c *Cpak) DisableAddon(app types.Application, origin string) error {
	origin = strings.ToLower(strings.TrimSpace(origin))
	enabled, err := loadEnabledAddons(app)
	if err != nil {
		return err
	}
	found := false
	remaining := make([]string, 0, len(enabled))
	for _, current := range enabled {
		if current == origin {
			found = true
			continue
		}
		remaining = append(remaining, current)
	}
	if !found {
		return fmt.Errorf("addon %s is not enabled for %s", origin, app.Name)
	}
	if err := saveEnabledAddons(app, remaining); err != nil {
		return err
	}
	if err := c.PrepareApplicationStorage(app); err != nil {
		return err
	}
	return c.StopContainer(app)
}

func (c *Cpak) addonUsers(origin string) ([]types.Application, error) {
	origin = strings.ToLower(origin)
	apps, err := c.GetInstalledApps()
	if err != nil {
		return nil, err
	}
	users := make([]types.Application, 0)
	for _, app := range apps {
		enabled, loadErr := loadEnabledAddons(app)
		if loadErr != nil {
			return nil, fmt.Errorf("read addons for %s: %w", app.Origin, loadErr)
		}
		for _, enabledOrigin := range enabled {
			if enabledOrigin == origin {
				users = append(users, app)
				break
			}
		}
	}
	return users, nil
}

func removeAddonConfiguration(app types.Application) error {
	path, err := addonConfigurationPath(app)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (c *Cpak) resolveEnabledAddons(app types.Application) ([]types.Application, error) {
	enabled, err := loadEnabledAddons(app)
	if err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return nil, nil
	}

	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return c.resolveEnabledAddonsFromStore(app, store)
}

func (c *Cpak) resolveEnabledAddonsFromStore(app types.Application, store *Store) ([]types.Application, error) {
	enabled, err := loadEnabledAddons(app)
	if err != nil {
		return nil, err
	}
	if len(enabled) == 0 {
		return nil, nil
	}
	enabledSet := make(map[string]bool, len(enabled))
	for _, origin := range enabled {
		if !supportedAddon(app, origin) {
			return nil, fmt.Errorf("enabled addon %s is no longer supported by %s", origin, app.Name)
		}
		enabledSet[origin] = true
	}

	addons := make([]types.Application, 0, len(enabled))
	seen := make(map[string]bool, len(enabled))
	for _, declaredOrigin := range app.ParsedAddons {
		origin := strings.ToLower(declaredOrigin)
		if !enabledSet[origin] || seen[origin] {
			continue
		}
		addon, getErr := store.GetApplicationByOrigin(origin, "", "main", "", "")
		if getErr != nil || addon.CpakId == "" {
			addon, getErr = store.GetApplicationByOrigin(origin, "", "", "", "")
		}
		if getErr != nil || addon.CpakId == "" {
			return nil, fmt.Errorf("enabled addon %s is not installed", origin)
		}
		// An addon carries what it declares as a layer dependency, and the
		// dependency has to sit under it exactly as it would under a parent.
		// Without this an addon can only ever contribute its own image, which
		// is why a bundle that names other packages composes to nothing.
		components, dependencyErr := c.resolveLayerDependenciesFromStore(addon, store)
		if dependencyErr != nil {
			return nil, fmt.Errorf("resolve what addon %s is built on: %w", origin, dependencyErr)
		}
		addons = append(addons, components...)
		addons = append(addons, addon)
		seen[origin] = true
	}
	return addons, nil
}

func combinedLayers(app types.Application, addons []types.Application) []string {
	seen := make(map[string]bool)
	layers := make([]string, 0, len(app.ParsedLayers))
	appendLayers := func(values []string) {
		for _, layer := range values {
			if layer == "" || seen[layer] {
				continue
			}
			seen[layer] = true
			layers = append(layers, layer)
		}
	}
	appendLayers(app.ParsedLayers)
	for _, addon := range addons {
		appendLayers(addon.ParsedLayers)
	}
	return layers
}

func addonPolicyIdentities(addons []types.Application) []addonPolicyIdentity {
	identities := make([]addonPolicyIdentity, 0, len(addons))
	for _, addon := range addons {
		identities = append(identities, addonPolicyIdentity{
			CpakID:      addon.CpakId,
			ImageDigest: addon.ImageDigest,
			Layers:      addon.ParsedLayers,
		})
	}
	return identities
}
