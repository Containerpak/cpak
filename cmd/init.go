/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type InitCmd struct {
	ManifestVersion string   `cli:"manifest-version,m" help:"Manifest version (default: 3.0)"`
	Name            string   `cli:"name,n" help:"Name of the application (required)"`
	Version         string   `cli:"version,v" help:"Version of the application, e.g. v1.0.0 (required)"`
	Description     string   `cli:"description,d" help:"Short description of the application (required)"`
	Image           string   `cli:"image,i" help:"OCI image reference (required)"`
	Binary          []string `cli:"binary,b" help:"Path to a binary to expose (can be repeated, must be absolute paths, required)"`
	DesktopEntry    []string `cli:"desktop-entry,e" help:"Path to a desktop entry file (can be repeated)"`
	FormFactor      []string `cli:"form-factor" help:"Supported device form factor (desktop, phone, tablet, tv or watch; can be repeated)"`
	Dependency      []string `cli:"dependency,D" help:"Origin of a cpak dependency (can be repeated)"`
	Addon           []string `cli:"addon,a" help:"Name of an addon (can be repeated)"`
	IdleTime        int      `cli:"idle-time,I" help:"Idle time in minutes after which to destroy the container"`

	cli.Base
}

func (c *InitCmd) Run() error {
	if c.Name == "" || c.Version == "" || c.Description == "" || c.Image == "" {
		return fmt.Errorf("name, version, description and image are mandatory")
	}

	manifestVersion := c.ManifestVersion
	if manifestVersion == "" {
		manifestVersion = "3.0"
	}
	schema := ""
	switch manifestVersion {
	case "2.0":
		schema = types.ManifestV2SchemaURL
	case "3.0":
		schema = types.ManifestV3SchemaURL
	}
	manifest := types.CpakManifest{
		Schema:          schema,
		ManifestVersion: manifestVersion,
		Name:            c.Name,
		Description:     c.Description,
		Version:         c.Version,
		Image:           c.Image,
		Binaries:        c.Binary,
		DesktopEntries:  c.DesktopEntry,
		FormFactors:     c.FormFactor,
		Dependencies:    []types.Dependency{},
		Addons:          c.Addon,
		IdleTime:        c.IdleTime,
		Override:        types.Override{},
	}
	for _, origin := range c.Dependency {
		manifest.Dependencies = append(manifest.Dependencies, types.Dependency{Origin: origin})
	}

	data, err := cpak.MarshalManifest(&manifest)
	if err != nil {
		return fmt.Errorf("cpak.json is invalid:\n%s", err)
	}
	if err := os.WriteFile("cpak.json", data, 0644); err != nil {
		return fmt.Errorf("failed to write cpak.json: %w", err)
	}

	c.Logger.Success("Created cpak.json successfully.")
	return nil
}
