/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SessionCmd struct {
	Action  string `arg:"action" help:"Action: list, enable, disable or launch"`
	Origin  string `arg:"origin" help:"Installed package origin"`
	Session string `arg:"session" help:"Session identifier"`
	Verbose bool   `cli:"verbose,v" help:"Enable verbose output"`
	Yes     bool   `cli:"yes,y" help:"Skip the confirmation prompt"`

	cli.Base
}

func (c *SessionCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	switch strings.ToLower(c.Action) {
	case "list":
		if c.Origin == "" {
			return fmt.Errorf("package origin is required for list")
		}
		origin, err := resolveApplicationOrigin(cp, c.Origin)
		if err != nil {
			return err
		}
		_, sessions, err := cp.Sessions(origin)
		if err != nil {
			return err
		}
		rows := make([][]string, 0, len(sessions))
		for _, session := range sessions {
			rows = append(rows, []string{session.ID, session.Name, session.Kind})
		}
		tools.ShowTable([]string{"ID", "Name", "Kind"}, rows)
		return nil
	case "enable":
		if c.Origin == "" || c.Session == "" {
			return fmt.Errorf("package origin and session identifier are required for enable")
		}
		origin, err := resolveApplicationOrigin(cp, c.Origin)
		if err != nil {
			return err
		}
		_, sessions, err := cp.Sessions(origin)
		if err != nil {
			return err
		}
		var selected *types.Session
		for index := range sessions {
			if sessions[index].ID == c.Session {
				selected = &sessions[index]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("session %s is not declared by the package", c.Session)
		}
		c.Logger.Info("The %s session will be available from the system login screen.", selected.Name)
		c.Logger.Info("The following permissions will be active in the session:")
		tools.PrintStructKeyVal(selected.Override)
		if !c.Yes && !tools.ConfirmOperation("Do you want to continue?") {
			return nil
		}
		if err := cp.EnableSession(origin, c.Session); err != nil {
			return err
		}
		c.Logger.Success("Session %s enabled", c.Session)
		return nil
	case "disable":
		id := c.Session
		if id == "" {
			id = c.Origin
		}
		if id == "" {
			return fmt.Errorf("session identifier is required for disable")
		}
		if !c.Yes && !tools.ConfirmOperation("Remove this session from the system login screen?") {
			return nil
		}
		if err := cp.DisableSession(id); err != nil {
			return err
		}
		c.Logger.Success("Session %s disabled", id)
		return nil
	case "launch":
		id := c.Session
		if id == "" {
			id = c.Origin
		}
		if id == "" {
			return fmt.Errorf("session identifier is required for launch")
		}
		return cp.RunSession(id, c.Verbose)
	default:
		return fmt.Errorf("unsupported session action %q", c.Action)
	}
}
