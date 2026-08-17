/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemCmd struct {
	Action string `arg:"action" help:"Action: setup, remove or status"`

	cli.Base
}

func (c *SystemCmd) Run() error {
	action := strings.ToLower(c.Action)
	switch action {
	case "status":
		if systemauthority.Installed() {
			c.Logger.Success("cpak system integration is installed")
			return nil
		}
		return fmt.Errorf("cpak system integration is not installed")
	case "setup", "remove":
		if os.Geteuid() != 0 {
			return runSystemSetup(action)
		}
		if action == "setup" {
			pending, err := systemauthority.Install()
			for _, note := range pending {
				c.Logger.Warning(note)
			}
			return err
		}
		return systemauthority.Uninstall()
	default:
		return fmt.Errorf("unsupported system action %q", c.Action)
	}
}

func runSystemSetup(action string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve cpak executable: %w", err)
	}
	path, err := exec.LookPath("pkexec")
	if err != nil {
		return fmt.Errorf("Polkit authentication is unavailable: %w", err)
	}
	command := exec.Command(path, executable, "system", action)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("system integration %s failed: %w", action, err)
	}
	return nil
}
