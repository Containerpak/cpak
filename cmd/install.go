/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type InstallCmd struct {
	Remote  string `arg:"remote" help:"Remote Git repository"`
	Branch  string `cli:"branch,b" help:"Specify a branch"`
	Release string `cli:"release,r" help:"Install a specific release"`
	Commit  string `cli:"commit,c" help:"Specify a commit"`
	Yes     bool   `cli:"yes,y" help:"Skip the confirmation prompt"`

	cli.Base
}

func (c *InstallCmd) Run() error {
	remote := strings.ToLower(c.Remote)

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while installing cpak: %s", err)
	}

	versionParams := []string{c.Branch, c.Release, c.Commit}
	versionParamsCount := 0
	for _, versionParam := range versionParams {
		if versionParam != "" {
			versionParamsCount++
		}
	}
	if versionParamsCount > 1 {
		return fmt.Errorf("more than one version parameter specified")
	}

	branch := c.Branch
	if versionParamsCount == 0 {
		branch, err = cp.GetDefaultBranch(remote)
		if err != nil {
			return err
		}
		c.Logger.Info("No version specified, using the default branch: %s", branch)
	}

	manifest, err := cp.FetchManifest(remote, branch, c.Release, c.Commit)
	if err != nil {
		return err
	}

	c.Logger.Info("\nThe following cpak(s) will be installed:")
	c.Logger.Info("  - %s: %s", manifest.Name, manifest.Description)
	c.Logger.Info("")

	c.Logger.Info("The following will be exported:")
	for _, binary := range manifest.Binaries {
		c.Logger.Info("  - (binary) %s", binary)
	}
	for _, entry := range manifest.DesktopEntries {
		c.Logger.Info("  - (desktop entry) %s", entry)
	}
	for _, session := range manifest.Sessions {
		c.Logger.Info("  - (%s session) %s", session.Kind, session.Name)
		tools.PrintStructKeyVal(session.Override)
	}
	c.Logger.Info("")

	c.Logger.Info("The following dependencies will be installed:")
	for _, dependency := range manifest.Dependencies {
		c.Logger.Info("  - %s", dependency)
	}
	c.Logger.Info("")

	if len(manifest.RuntimeSources) > 0 {
		c.Logger.Info("The following files will be downloaded from third parties:")
		for _, source := range manifest.RuntimeSources {
			c.Logger.Info("  - %s (%d bytes)", source.URL, source.Size)
		}
		c.Logger.Info("")
	}

	c.Logger.Info("The following permissions will be granted:")
	tools.PrintStructKeyVal(manifest.Override)
	c.Logger.Info("")

	if !c.Yes && !tools.ConfirmOperation("Do you want to continue?") {
		return nil
	}

	return cp.InstallCpak(remote, manifest, branch, c.Commit, c.Release)
}
