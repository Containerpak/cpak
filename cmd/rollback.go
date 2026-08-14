/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type RollbackCmd struct {
	Remote string `arg:"remote" help:"Remote Git repository of the installed package"`

	cli.Base
}

func (c *RollbackCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("open cpak store: %w", err)
	}
	result, err := cp.Rollback(strings.ToLower(c.Remote))
	if err != nil {
		return fmt.Errorf("rollback %s: %w", c.Remote, err)
	}
	c.Logger.Info("Rolled back %s from %s to %s.", result.Name, result.FromVersion, result.ToVersion)
	return nil
}
