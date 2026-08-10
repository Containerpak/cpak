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

type StopCmd struct {
	Remote  string `arg:"remote" help:"Remote Git repository"`
	Version string `cli:"version,v" help:"Specify a version"`
	Branch  string `cli:"branch,b" help:"Specify a branch"`
	Commit  string `cli:"commit,c" help:"Specify a commit"`
	Release string `cli:"release,r" help:"Specify a release"`

	cli.Base
}

func (c *StopCmd) Run() error {
	c.Logger.Info("Stopping cpak from remote: %s", c.Remote)

	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}

	err = cp.Stop(c.Remote, c.Version, c.Branch, c.Commit, c.Release)
	if err != nil {
		return fmt.Errorf("an error occurred while stopping the cpak container: %s", err)
	}

	return nil
}
