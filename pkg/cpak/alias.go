/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const aliasConfigurationVersion = 1

var aliasNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type aliasConfiguration struct {
	Version int               `json:"version"`
	Aliases map[string]string `json:"aliases"`
}

func (c *Cpak) SetAlias(name, origin string) error {
	name, err := normalizeAliasName(name)
	if err != nil {
		return err
	}
	origin, err = normalizeAliasOrigin(origin)
	if err != nil {
		return err
	}
	if err = c.requireInstalledOrigin(origin, ""); err != nil {
		return err
	}
	configuration, err := c.loadAliasConfiguration()
	if err != nil {
		return err
	}
	configuration.Aliases[name] = origin
	return c.saveAliasConfiguration(configuration)
}

func (c *Cpak) RemoveAlias(name string) error {
	name, err := normalizeAliasName(name)
	if err != nil {
		return err
	}
	configuration, err := c.loadAliasConfiguration()
	if err != nil {
		return err
	}
	if _, exists := configuration.Aliases[name]; !exists {
		return fmt.Errorf("alias not found: %s", name)
	}
	delete(configuration.Aliases, name)
	return c.saveAliasConfiguration(configuration)
}

func (c *Cpak) ListAliases() ([]types.Alias, error) {
	configuration, err := c.loadAliasConfiguration()
	if err != nil {
		return nil, err
	}
	aliases := make([]types.Alias, 0, len(configuration.Aliases))
	for name, origin := range configuration.Aliases {
		aliases = append(aliases, types.Alias{Name: name, Origin: origin})
	}
	sort.Slice(aliases, func(i, j int) bool {
		return aliases[i].Name < aliases[j].Name
	})
	return aliases, nil
}

func (c *Cpak) ResolveOrigin(value string) (string, error) {
	origin, _, err := c.resolveOrigin(value)
	return origin, err
}

func (c *Cpak) ResolveInstalledOrigin(value string) (string, error) {
	origin, alias, err := c.resolveOrigin(value)
	if err != nil {
		return "", err
	}
	if err = c.requireInstalledOrigin(origin, alias); err != nil {
		return "", err
	}
	return origin, nil
}

func (c *Cpak) resolveOrigin(value string) (string, string, error) {
	name, aliasErr := normalizeAliasName(value)
	if aliasErr == nil {
		configuration, err := c.loadAliasConfiguration()
		if err != nil {
			return "", "", err
		}
		origin, exists := configuration.Aliases[name]
		if !exists {
			return "", "", fmt.Errorf("alias not found: %s", name)
		}
		return origin, name, nil
	}
	origin, err := normalizeAliasOrigin(value)
	if err != nil {
		return "", "", err
	}
	return origin, "", nil
}

func (c *Cpak) requireInstalledOrigin(origin, alias string) error {
	store, err := NewStore(c.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, "", "", "", "")
	if err == nil && app.CpakId != "" {
		return nil
	}
	if alias != "" {
		return fmt.Errorf("alias %q refers to an origin that is not installed: %s", alias, origin)
	}
	return fmt.Errorf("application is not installed: %s", origin)
}

func (c *Cpak) aliasConfigurationPath() string {
	return filepath.Join(c.Options.StorePath, "config", "aliases.json")
}

func (c *Cpak) loadAliasConfiguration() (aliasConfiguration, error) {
	configuration := aliasConfiguration{Version: aliasConfigurationVersion, Aliases: map[string]string{}}
	path := c.aliasConfigurationPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return configuration, nil
	}
	if err != nil {
		return configuration, fmt.Errorf("read alias storage: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return configuration, fmt.Errorf("alias storage permissions must be 0600: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return configuration, fmt.Errorf("read alias storage: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&configuration); err != nil {
		return aliasConfiguration{}, fmt.Errorf("alias storage is corrupted: %w", err)
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return aliasConfiguration{}, errors.New("alias storage is corrupted: multiple JSON values")
	}
	if configuration.Version != aliasConfigurationVersion || configuration.Aliases == nil {
		return aliasConfiguration{}, errors.New("alias storage is corrupted: unsupported configuration")
	}
	for name, origin := range configuration.Aliases {
		canonicalName, nameErr := normalizeAliasName(name)
		canonicalOrigin, originErr := normalizeAliasOrigin(origin)
		if nameErr != nil || originErr != nil || canonicalName != name || canonicalOrigin != origin {
			return aliasConfiguration{}, errors.New("alias storage is corrupted: invalid alias entry")
		}
	}
	return configuration, nil
}

func (c *Cpak) saveAliasConfiguration(configuration aliasConfiguration) error {
	path := c.aliasConfigurationPath()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create alias storage: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return fmt.Errorf("secure alias storage: %w", err)
	}
	data, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(directory, ".aliases-")
	if err != nil {
		return fmt.Errorf("create alias storage: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure alias storage: %w", err)
	}
	if _, err = temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write alias storage: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync alias storage: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close alias storage: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace alias storage: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open alias storage: %w", err)
	}
	defer directoryHandle.Close()
	if err = directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync alias storage: %w", err)
	}
	return nil
}

func normalizeAliasName(name string) (string, error) {
	if name != strings.TrimSpace(name) {
		return "", fmt.Errorf("invalid alias name %q: use 1-32 letters, digits, or hyphens", name)
	}
	name = strings.ToLower(name)
	if !aliasNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid alias name %q: use 1-32 letters, digits, or hyphens", name)
	}
	return name, nil
}

func normalizeAliasOrigin(origin string) (string, error) {
	if origin != strings.TrimSpace(origin) {
		return "", fmt.Errorf("invalid origin %q", origin)
	}
	origin = strings.ToLower(origin)
	parts := strings.Split(origin, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid origin %q", origin)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\ \t\n\r") {
			return "", fmt.Errorf("invalid origin %q", origin)
		}
	}
	return origin, nil
}
