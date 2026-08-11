/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"golang.org/x/sys/unix"
)

type LaunchCmd struct {
	ExtraArgs []string `arg:"extra" help:"command and arguments"`

	cli.Base
}

func (c *LaunchCmd) Run() error {
	if len(c.ExtraArgs) == 0 {
		return fmt.Errorf("command is required")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("enable no new privileges: %w", err)
	}
	path, err := exec.LookPath(c.ExtraArgs[0])
	if err != nil {
		return err
	}
	return syscall.Exec(path, c.ExtraArgs, os.Environ())
}
