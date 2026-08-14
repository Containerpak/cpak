/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LaunchCmd struct {
	UserNamespaces     bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
	AllowPtrace        bool     `cli:"allow-ptrace" help:"allow tracing inside a private process namespace"`
	RequireSandbox     bool     `cli:"require-sandbox" help:"fail if filesystem or syscall restrictions are unavailable"`
	LandlockReadOnly   []string `cli:"landlock-read-only" help:"grant read-only filesystem access"`
	LandlockWriteFiles []string `cli:"landlock-write-files" help:"grant writes to existing files"`
	LandlockReadWrite  []string `cli:"landlock-read-write" help:"grant read-write filesystem access"`
	ExtraArgs          []string `arg:"extra" help:"command and arguments"`

	cli.Base
}

func (c *LaunchCmd) Run() error {
	if len(c.ExtraArgs) == 0 {
		return fmt.Errorf("command is required")
	}
	grants := c.landlockGrants()
	if len(grants) == 0 {
		return fmt.Errorf("landlock grants are required")
	}
	if _, err := sandbox.ApplyLandlock(grants); err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) && !c.RequireSandbox {
			c.Logger.Warning("Landlock is unavailable; continuing without filesystem restrictions")
		} else {
			return err
		}
	}
	if err := sandbox.ApplySeccomp(c.UserNamespaces, c.AllowPtrace); err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) && !c.RequireSandbox {
			c.Logger.Warning("Seccomp is unavailable; continuing without syscall restrictions")
		} else {
			return err
		}
	}
	path, err := exec.LookPath(c.ExtraArgs[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, c.ExtraArgs, os.Environ())
}

func (c *LaunchCmd) landlockGrants() []sandbox.PathGrant {
	grants := make([]sandbox.PathGrant, 0, len(c.LandlockReadOnly)+len(c.LandlockWriteFiles)+len(c.LandlockReadWrite))
	for _, path := range c.LandlockReadOnly {
		grants = append(grants, sandbox.PathGrant{Path: path, ReadOnly: true})
	}
	for _, path := range c.LandlockWriteFiles {
		grants = append(grants, sandbox.PathGrant{Path: path, ReadOnly: true, WriteFiles: true})
	}
	for _, path := range c.LandlockReadWrite {
		grants = append(grants, sandbox.PathGrant{Path: path})
	}
	return grants
}
