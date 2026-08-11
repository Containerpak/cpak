/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type InitCmd struct {
	ManifestVersion string   `cli:"manifest-version,m" help:"Manifest version (default: 2.0)"`
	Name            string   `cli:"name,n" help:"Name of the application (required)"`
	Version         string   `cli:"version,v" help:"Version of the application, e.g. v1.0.0 (required)"`
	Description     string   `cli:"description,d" help:"Short description of the application (required)"`
	Image           string   `cli:"image,i" help:"OCI image reference (required)"`
	Binary          []string `cli:"binary,b" help:"Path to a binary to expose (can be repeated, must be absolute paths, required)"`
	DesktopEntry    []string `cli:"desktop-entry,e" help:"Path to a desktop entry file (can be repeated)"`
	Dependency      []string `cli:"dependency,D" help:"Origin of a cpak dependency (can be repeated)"`
	Addon           []string `cli:"addon,a" help:"Name of an addon (can be repeated)"`
	IdleTime        int      `cli:"idle-time,I" help:"Idle time in minutes after which to destroy the container"`

	cli.Base
}

func (c *InitCmd) Run() error {
	if c.Name == "" || c.Version == "" || c.Description == "" || c.Image == "" {
		return fmt.Errorf("name, version, description and image are mandatory")
	}

	manifest := types.CpakManifest{
		ManifestVersion: c.ManifestVersion,
		Name:            c.Name,
		Description:     c.Description,
		Version:         c.Version,
		Image:           c.Image,
		Binaries:        c.Binary,
		DesktopEntries:  c.DesktopEntry,
		Dependencies:    []types.Dependency{},
		Addons:          c.Addon,
		IdleTime:        c.IdleTime,
		Override:        types.Override{},
	}
	if manifest.ManifestVersion == "" {
		manifest.ManifestVersion = "2.0"
	}
	for _, origin := range c.Dependency {
		manifest.Dependencies = append(manifest.Dependencies, types.Dependency{Origin: origin})
	}

	if err := cpak.ValidateManifest(&manifest); err != nil {
		return fmt.Errorf("cpak.json is invalid:\n%s", err)
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize manifest: %w", err)
	}
	if err := os.WriteFile("cpak.json", data, 0644); err != nil {
		return fmt.Errorf("failed to write cpak.json: %w", err)
	}

	c.Logger.Success("Created cpak.json successfully.")
	return nil
}
