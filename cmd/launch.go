/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LaunchCmd struct {
	UserNamespaces     bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
	AllowPtrace        bool     `cli:"allow-ptrace" help:"allow tracing inside a private process namespace"`
	RequireSandbox     bool     `cli:"require-sandbox" help:"deprecated; filesystem and syscall restrictions are always required"`
	LandlockReadOnly   []string `cli:"landlock-read-only" help:"grant read-only filesystem access"`
	LandlockWriteFiles []string `cli:"landlock-write-files" help:"grant writes to existing files"`
	LandlockReadWrite  []string `cli:"landlock-read-write" help:"grant read-write filesystem access"`
	ExtraArgs          []string `arg:"extra" help:"command and arguments"`

	cli.Base
}

func (c *LaunchCmd) Run() error {
	logger.ProxyMode()
	if len(c.ExtraArgs) == 0 {
		return fmt.Errorf("command is required")
	}
	grants := c.landlockGrants()
	if len(grants) == 0 {
		return fmt.Errorf("landlock grants are required: launch runs a command inside an existing sandbox, use cpak run to start a package")
	}
	_, landlockErr := sandbox.ApplyLandlock(grants)
	if err := sandboxOutcome(landlockErr, "Landlock", "boot the kernel with landlock in its lsm= list"); err != nil {
		return err
	}
	if err := sandboxOutcome(sandbox.ApplySeccomp(c.UserNamespaces, c.AllowPtrace), "Seccomp", "the kernel needs CONFIG_SECCOMP_FILTER and the sandbox around cpak must allow prctl(PR_SET_SECCOMP)"); err != nil {
		return err
	}
	path, err := exec.LookPath(c.ExtraArgs[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, c.ExtraArgs, os.Environ())
}

func sandboxOutcome(err error, name, remedy string) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, sandbox.ErrUnavailable) {
		return err
	}
	return fmt.Errorf("%s is unavailable, so the application was not started: %s", name, remedy)
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
