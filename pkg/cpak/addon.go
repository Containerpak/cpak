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

const addonConfigurationVersion = 3

// The manifest says what a package was built to accept, and that is a
// statement by whoever made it. Chosen is the other kind of selection: an
// addon the owner of the machine put there themselves, past what the package
// offers. The two are kept apart because they answer to different people, and
// because a publisher dropping an addon must not silently keep composing it
// while an addon the owner chose must survive exactly that.
type addonConfiguration struct {
	Version  int               `json:"version"`
	Enabled  []string          `json:"enabled"`
	Chosen   []string          `json:"chosen,omitempty"`
	Disabled []string          `json:"disabled,omitempty"`
	Slots    map[string]string `json:"slots,omitempty"`
}

type AddonStatus struct {
	Origin    string `json:"origin"`
	Enabled   bool   `json:"enabled"`
	Installed bool   `json:"installed"`
	Provider  string `json:"provider,omitempty"`
	Slot      string `json:"slot,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

type AddonSlotStatus struct {
	Slot      string   `json:"slot"`
	Mode      string   `json:"mode"`
	Active    []string `json:"active"`
	Available []string `json:"available"`
}

type addonPolicyIdentity struct {
	CpakID      string               `json:"cpak_id"`
	ImageDigest string               `json:"image_digest"`
	Layers      []string             `json:"layers"`
	Provider    *types.AddonProvider `json:"provider,omitempty"`
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

func loadAddonConfiguration(app types.Application) (addonConfiguration, error) {
	path, err := addonConfigurationPath(app)
	if err != nil {
		return addonConfiguration{}, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return addonConfiguration{}, nil
	}
	if err != nil {
		return addonConfiguration{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var configuration addonConfiguration
	if err := decoder.Decode(&configuration); err != nil {
		return addonConfiguration{}, fmt.Errorf("decode addon configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return addonConfiguration{}, fmt.Errorf("decode addon configuration: trailing data")
	}
	// Version 1 knew only what the manifest offered, so everything it holds is
	// that, and it is read rather than refused.
	if configuration.Version != 1 && configuration.Version != 2 && configuration.Version != addonConfigurationVersion {
		return addonConfiguration{}, fmt.Errorf("unsupported addon configuration version %d", configuration.Version)
	}
	return configuration, nil
}

// loadEnabledAddons answers with everything that is on, whoever turned it on.
func loadEnabledAddons(app types.Application) ([]string, error) {
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return nil, err
	}
	return normalizedOrigins(append(append([]string{}, configuration.Enabled...), configuration.Chosen...)), nil
}

// loadChosenAddons answers with the ones the owner put there themselves.
func loadChosenAddons(app types.Application) (map[string]bool, error) {
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return nil, err
	}
	chosen := make(map[string]bool, len(configuration.Chosen))
	for _, origin := range normalizedOrigins(configuration.Chosen) {
		chosen[origin] = true
	}
	return chosen, nil
}

func saveEnabledAddons(app types.Application, enabled []string) error {
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return err
	}
	chosen := make(map[string]bool, len(configuration.Chosen))
	for _, origin := range normalizedOrigins(configuration.Chosen) {
		chosen[origin] = true
	}
	keep := make([]string, 0, len(configuration.Chosen))
	offered := make([]string, 0, len(enabled))
	for _, origin := range normalizedOrigins(enabled) {
		if chosen[origin] {
			keep = append(keep, origin)
			continue
		}
		offered = append(offered, origin)
	}
	configuration.Enabled = offered
	configuration.Chosen = keep
	return saveAddonConfiguration(app, configuration)
}

func writeAddonConfiguration(app types.Application, enabled, chosen []string) error {
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return err
	}
	configuration.Enabled = enabled
	configuration.Chosen = chosen
	return saveAddonConfiguration(app, configuration)
}

func saveAddonConfiguration(app types.Application, configuration addonConfiguration) error {
	path, err := addonConfigurationPath(app)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	configuration.Version = addonConfigurationVersion
	configuration.Enabled = normalizedOrigins(configuration.Enabled)
	configuration.Chosen = normalizedOrigins(configuration.Chosen)
	configuration.Disabled = normalizedOrigins(configuration.Disabled)
	if len(configuration.Slots) == 0 {
		configuration.Slots = nil
	} else {
		normalized := make(map[string]string, len(configuration.Slots))
		for slot, provider := range configuration.Slots {
			slot = strings.ToLower(strings.TrimSpace(slot))
			provider = strings.ToLower(strings.TrimSpace(provider))
			if slot != "" && provider != "" {
				normalized[slot] = provider
			}
		}
		configuration.Slots = normalized
	}
	data, err := json.MarshalIndent(configuration, "", "  ")
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
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	active, err := c.activeAddonCandidatesFromStore(app, store)
	if err != nil {
		return nil, err
	}
	activeSet := make(map[string]bool, len(active))
	for _, addon := range active {
		activeSet[addon.Origin] = true
	}
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return nil, err
	}
	origins := append(append([]string{}, app.ParsedAddons...), configuration.Chosen...)

	statuses := make([]AddonStatus, 0, len(origins))
	for _, origin := range orderedOrigins(origins) {
		installed, getErr := installedAddon(store, origin)
		status := AddonStatus{
			Origin:    origin,
			Enabled:   activeSet[origin],
			Installed: getErr == nil && installed.CpakId != "",
		}
		if status.Installed && installed.ParsedAddonProvider != nil {
			status.Provider = installed.ParsedAddonProvider.ID
			status.Slot = installed.ParsedAddonProvider.Slot
			status.Mode = installed.ParsedAddonProvider.Mode
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (c *Cpak) AddonSlots(app types.Application) ([]AddonSlotStatus, error) {
	statuses, err := c.AddonStatuses(app)
	if err != nil {
		return nil, err
	}
	bySlot := make(map[string]*AddonSlotStatus)
	order := make([]string, 0)
	for _, status := range statuses {
		if status.Slot == "" {
			continue
		}
		slot := bySlot[status.Slot]
		if slot == nil {
			slot = &AddonSlotStatus{Slot: status.Slot, Mode: status.Mode}
			bySlot[status.Slot] = slot
			order = append(order, status.Slot)
		}
		if slot.Mode != status.Mode {
			return nil, fmt.Errorf("addon slot %s mixes %s and %s providers", status.Slot, slot.Mode, status.Mode)
		}
		if status.Installed {
			slot.Available = append(slot.Available, status.Origin)
		}
		if status.Enabled {
			slot.Active = append(slot.Active, status.Origin)
		}
	}
	slots := make([]AddonSlotStatus, 0, len(order))
	for _, name := range order {
		slots = append(slots, *bySlot[name])
	}
	return slots, nil
}

func (c *Cpak) SelectAddonProvider(app types.Application, slot, provider string) error {
	slot = strings.ToLower(strings.TrimSpace(slot))
	provider = strings.ToLower(strings.TrimSpace(provider))
	statuses, err := c.AddonStatuses(app)
	if err != nil {
		return err
	}
	matches := make([]AddonStatus, 0, 1)
	for _, status := range statuses {
		if status.Slot == slot && status.Installed && (status.Origin == provider || status.Provider == provider) {
			matches = append(matches, status)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("provider %s is not installed for addon slot %s", provider, slot)
	}
	if len(matches) > 1 {
		return fmt.Errorf("provider %s is ambiguous in addon slot %s; use its origin", provider, slot)
	}
	selected := matches[0]
	if selected.Mode != types.AddonSlotExclusive {
		return fmt.Errorf("addon slot %s accepts multiple providers and does not need a selection", slot)
	}
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return err
	}
	if configuration.Slots == nil {
		configuration.Slots = make(map[string]string)
	}
	configuration.Slots[slot] = selected.Origin
	configuration.Disabled = withoutOrigin(configuration.Disabled, selected.Origin)
	if supportedAddon(app, selected.Origin) {
		configuration.Enabled = append(configuration.Enabled, selected.Origin)
	} else {
		configuration.Chosen = append(configuration.Chosen, selected.Origin)
	}
	if err := saveAddonConfiguration(app, configuration); err != nil {
		return err
	}
	if err := c.PrepareApplicationStorage(app); err != nil {
		return err
	}
	return c.StopContainer(app)
}

func (c *Cpak) EnableAddon(app types.Application, origin string) error {
	return c.enableAddon(app, origin, false)
}

// EnableChosenAddon turns on an addon the package does not offer. The manifest
// is the publisher saying what they tested together, and it is not a rule the
// owner of a machine has to obey: what it costs is that nobody but them
// answers for the combination, so it is a separate call and never a fallback
// from the ordinary one.
func (c *Cpak) EnableChosenAddon(app types.Application, origin string) error {
	return c.enableAddon(app, origin, true)
}

func (c *Cpak) enableAddon(app types.Application, origin string, chosen bool) error {
	origin = strings.ToLower(strings.TrimSpace(origin))
	if !supportedAddon(app, origin) && !chosen {
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
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return err
	}
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	installed, getErr := installedAddon(store, origin)
	closeErr := store.Close()
	if getErr != nil {
		return fmt.Errorf("read installed addon %s: %w", origin, getErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if chosen && !supportedAddon(app, origin) {
		configuration.Chosen = append(configuration.Chosen, origin)
	} else {
		configuration.Enabled = append(configuration.Enabled, origin)
	}
	configuration.Disabled = withoutOrigin(configuration.Disabled, origin)
	if provider := installed.ParsedAddonProvider; provider != nil && provider.Mode == types.AddonSlotExclusive {
		if configuration.Slots == nil {
			configuration.Slots = make(map[string]string)
		}
		configuration.Slots[provider.Slot] = origin
	}
	if err := saveAddonConfiguration(app, configuration); err != nil {
		return err
	}
	if err := c.PrepareApplicationStorage(app); err != nil {
		return err
	}
	return c.StopContainer(app)
}

func (c *Cpak) DisableAddon(app types.Application, origin string) error {
	origin = strings.ToLower(strings.TrimSpace(origin))
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	active, err := c.activeAddonCandidatesFromStore(app, store)
	if err != nil {
		_ = store.Close()
		return err
	}
	installed, getErr := installedAddon(store, origin)
	closeErr := store.Close()
	found := false
	for _, addon := range active {
		found = found || addon.Origin == origin
	}
	if !found {
		return fmt.Errorf("addon %s is not enabled for %s", origin, app.Name)
	}
	if getErr != nil {
		return getErr
	}
	if closeErr != nil {
		return closeErr
	}
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return err
	}
	configuration.Enabled = withoutOrigin(configuration.Enabled, origin)
	configuration.Chosen = withoutOrigin(configuration.Chosen, origin)
	if provider := installed.ParsedAddonProvider; provider != nil {
		configuration.Disabled = append(configuration.Disabled, origin)
		if selected := configuration.Slots[provider.Slot]; selected == origin || selected == provider.ID {
			delete(configuration.Slots, provider.Slot)
		}
	}
	if err := saveAddonConfiguration(app, configuration); err != nil {
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
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	for _, app := range apps {
		configured, loadErr := loadEnabledAddons(app)
		if loadErr != nil {
			return nil, fmt.Errorf("read addons for %s: %w", app.Origin, loadErr)
		}
		if originSet(configured)[origin] {
			users = append(users, app)
			continue
		}
		enabled, loadErr := c.activeAddonCandidatesFromStore(app, store)
		if loadErr != nil {
			return nil, fmt.Errorf("read addons for %s: %w", app.Origin, loadErr)
		}
		for _, addon := range enabled {
			if addon.Origin == origin {
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
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return c.resolveEnabledAddonsFromStore(app, store)
}

func (c *Cpak) resolveEnabledAddonsFromStore(app types.Application, store *Store) ([]types.Application, error) {
	active, err := c.activeAddonCandidatesFromStore(app, store)
	if err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return nil, nil
	}
	addons := make([]types.Application, 0, len(active))
	for _, addon := range active {
		components, dependencyErr := c.resolveLayerDependenciesFromStore(addon, store)
		if dependencyErr != nil {
			return nil, fmt.Errorf("resolve what addon %s is built on: %w", addon.Origin, dependencyErr)
		}
		addons = append(addons, components...)
		addons = append(addons, addon)
	}
	return addons, nil
}

type addonCandidate struct {
	application types.Application
	explicit    bool
}

func (c *Cpak) activeAddonCandidatesFromStore(app types.Application, store *Store) ([]types.Application, error) {
	configuration, err := loadAddonConfiguration(app)
	if err != nil {
		return nil, err
	}
	chosen := originSet(configuration.Chosen)
	explicit := originSet(append(append([]string{}, configuration.Enabled...), configuration.Chosen...))
	disabled := originSet(configuration.Disabled)
	for _, origin := range configuration.Enabled {
		if !supportedAddon(app, origin) {
			return nil, fmt.Errorf("enabled addon %s is no longer supported by %s", origin, app.Name)
		}
	}

	origins := orderedOrigins(append(append([]string{}, app.ParsedAddons...), configuration.Chosen...))
	candidates := make([]addonCandidate, 0, len(origins))
	for _, origin := range origins {
		if disabled[origin] {
			continue
		}
		addon, getErr := installedAddon(store, origin)
		if getErr != nil || addon.CpakId == "" {
			if explicit[origin] {
				return nil, fmt.Errorf("enabled addon %s is not installed", origin)
			}
			continue
		}
		if addon.ParsedAddonProvider == nil && !explicit[origin] {
			continue
		}
		if !supportedAddon(app, origin) && !chosen[origin] {
			continue
		}
		candidates = append(candidates, addonCandidate{application: addon, explicit: explicit[origin]})
	}

	type slotGroup struct {
		mode       string
		candidates []addonCandidate
	}
	groups := make(map[string]*slotGroup)
	for _, candidate := range candidates {
		provider := candidate.application.ParsedAddonProvider
		if provider == nil {
			continue
		}
		group := groups[provider.Slot]
		if group == nil {
			group = &slotGroup{mode: provider.Mode}
			groups[provider.Slot] = group
		}
		if group.mode != provider.Mode {
			return nil, fmt.Errorf("addon slot %s mixes %s and %s providers", provider.Slot, group.mode, provider.Mode)
		}
		group.candidates = append(group.candidates, candidate)
	}

	active := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate.application.ParsedAddonProvider == nil {
			active[candidate.application.Origin] = true
		}
	}
	for slot, group := range groups {
		if group.mode == types.AddonSlotMultiple {
			for _, candidate := range group.candidates {
				active[candidate.application.Origin] = true
			}
			continue
		}
		selection := strings.ToLower(configuration.Slots[slot])
		selected := -1
		if selection != "" {
			for index, candidate := range group.candidates {
				provider := candidate.application.ParsedAddonProvider
				if candidate.application.Origin == selection || provider.ID == selection {
					if selected != -1 {
						return nil, fmt.Errorf("provider %s is ambiguous in addon slot %s", selection, slot)
					}
					selected = index
				}
			}
			if selected == -1 {
				return nil, fmt.Errorf("selected provider %s is not available for addon slot %s", selection, slot)
			}
		} else {
			for index, candidate := range group.candidates {
				if candidate.explicit {
					selected = index
					break
				}
			}
			if selected == -1 {
				selected = 0
			}
		}
		active[group.candidates[selected].application.Origin] = true
	}

	result := make([]types.Application, 0, len(active))
	for _, candidate := range candidates {
		if active[candidate.application.Origin] {
			result = append(result, candidate.application)
		}
	}
	return result, nil
}

func installedAddon(store *Store, origin string) (types.Application, error) {
	addon, err := store.GetApplicationByOrigin(origin, "", "main", "", "")
	if err == nil && addon.CpakId != "" {
		return addon, nil
	}
	return store.GetApplicationByOrigin(origin, "", "", "", "")
}

func originSet(origins []string) map[string]bool {
	set := make(map[string]bool, len(origins))
	for _, origin := range normalizedOrigins(origins) {
		set[origin] = true
	}
	return set
}

func orderedOrigins(origins []string) []string {
	seen := make(map[string]bool, len(origins))
	ordered := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.ToLower(strings.TrimSpace(origin))
		if origin == "" || seen[origin] {
			continue
		}
		seen[origin] = true
		ordered = append(ordered, origin)
	}
	return ordered
}

func withoutOrigin(origins []string, remove string) []string {
	remaining := make([]string, 0, len(origins))
	for _, origin := range normalizedOrigins(origins) {
		if origin != remove {
			remaining = append(remaining, origin)
		}
	}
	return remaining
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
			Provider:    addon.ParsedAddonProvider,
		})
	}
	return identities
}
