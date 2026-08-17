/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemCmd struct {
	Action      string `arg:"action" help:"Action: setup, remove, status, register-session or remove-session"`
	ID          string `cli:"id" help:"Session identifier for the session actions"`
	Origin      string `cli:"origin" help:"Package origin for the session actions"`
	Name        string `cli:"name" help:"Session name for register-session"`
	Description string `cli:"description" help:"Session description for register-session"`
	Kind        string `cli:"kind" help:"Session kind for register-session"`

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
	case "register-session", "remove-session":
		// These carry a step already validated against the store of the user
		// who asked for it, so they only ever run through the escalation.
		if os.Geteuid() != 0 {
			return fmt.Errorf("%s requires root", action)
		}
		if action == "register-session" {
			return systemauthority.Register(systemauthority.Session{
				ID:          c.ID,
				Origin:      c.Origin,
				Name:        c.Name,
				Description: c.Description,
				Kind:        c.Kind,
			})
		}
		registered, err := systemauthority.DefaultRegistry().Load(c.ID)
		if err != nil {
			return err
		}
		return systemauthority.Remove(registered.ID, registered.Origin)
	default:
		return fmt.Errorf("unsupported system action %q", c.Action)
	}
}

func runSystemSetup(action string) error {
	if err := runPrivileged("system", action); err != nil {
		return fmt.Errorf("system integration %s failed: %w", action, err)
	}
	return nil
}
