/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type RunCmd struct {
	Remote string   `arg:"remote" help:"Remote Git repository"`
	Binary string   `arg:"binary" help:"Binary to launch"`
	Extra  []string `arg:"extra" help:"Extra arguments for the binary"`

	Verbose       bool   `cli:"verbose,v" help:"Enable verbose output"`
	Instance      string `cli:"instance,i" help:"Application instance"`
	Branch        string `cli:"branch,b" help:"Specify a branch"`
	Commit        string `cli:"commit,c" help:"Specify a commit"`
	Release       string `cli:"release,r" help:"Specify a release"`
	NestedRequest string `cli:"nested-request" help:"Run an encoded request from the cpak service"`

	cli.Base
}

func (c *RunCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return c.runError(err)
	}
	if c.NestedRequest != "" {
		params, decodeErr := cpak.DecodeNestedRequest(c.NestedRequest)
		if decodeErr != nil {
			return c.runError(decodeErr)
		}
		return c.runError(cp.RunAuthorized(params, c.Verbose))
	}

	remote, err := resolveApplicationOrigin(cp, c.Remote)
	if err != nil {
		return c.runError(err)
	}
	logger.Println("Running cpak from remote:", remote)

	err = cp.RunInstance(remote, "", c.Branch, c.Commit, c.Release, c.Instance, c.Binary, c.Verbose, c.Extra...)
	if err != nil {
		return c.runError(err)
	}

	return nil
}

func (c *RunCmd) runError(iErr error) error {
	if iErr == nil {
		return nil
	}
	return fmt.Errorf("an error occurred while running cpak: %w", iErr)
}
