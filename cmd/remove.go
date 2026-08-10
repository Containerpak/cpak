/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
	remote := c.Remote

	cp, err := cpak.NewCpak()
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

	err = cp.Remove(remote, branch, c.Release, c.Commit)
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
	}

	c.Logger.Success("Cpak %s removed", remote)
	return nil
}
