/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type RemoveCmd struct {
	Remote  string `arg:"remote" help:"Remote Git repository"`
	Branch  string `cli:"branch,b" help:"Specify a branch"`
	Release string `cli:"release,r" help:"Install a specific release"`
	Commit  string `cli:"commit,c" help:"Specify a commit"`

	cli.Base
}

func (c *RemoveCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
	}
	remote, err := resolveApplicationOrigin(cp, c.Remote)
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
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
		c.Logger.Info("No version specified, using main branch if available")
		branch = "main"
	}

	err = cp.Remove(remote, branch, c.Commit, c.Release)
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
	}

	c.Logger.Success("cpak %s removed", remote)
	return nil
}
