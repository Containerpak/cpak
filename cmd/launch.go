/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
	UserNamespaces bool     `cli:"user-namespaces" help:"allow application-created user namespaces"`
	ExtraArgs      []string `arg:"extra" help:"command and arguments"`

	cli.Base
}

func (c *LaunchCmd) Run() error {
	if len(c.ExtraArgs) == 0 {
		return fmt.Errorf("command is required")
	}
	if err := sandbox.ApplySeccomp(c.UserNamespaces); err != nil {
		if errors.Is(err, sandbox.ErrUnavailable) {
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
