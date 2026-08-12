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
			return systemauthority.Install()
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
