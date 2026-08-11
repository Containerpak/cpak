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

type ShellCmd struct {
	Remote   string `arg:"remote" help:"Remote Git repository"`
	Verbose  bool   `cli:"verbose,v" help:"Enable verbose output"`
	Instance string `cli:"instance,i" help:"Application instance"`
	Branch   string `cli:"branch,b" help:"Specify a branch"`
	Commit   string `cli:"commit,c" help:"Specify a commit"`
	Release  string `cli:"release,r" help:"Specify a release"`

	cli.Base
}

func (c *ShellCmd) Run() error {
	binary := "@sh"

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while opening the cpak shell: %s", err)
	}
	remote, err := resolveApplicationOrigin(cp, c.Remote)
	if err != nil {
		return fmt.Errorf("an error occurred while opening the cpak shell: %s", err)
	}

	c.Logger.Info("Running cpak from remote: %s", remote)

	err = cp.RunInstance(remote, c.Branch, c.Branch, c.Commit, c.Release, c.Instance, binary, c.Verbose, "-i")
	if err != nil {
		return fmt.Errorf("an error occurred while opening the cpak shell: %s", err)
	}

	return nil
}
