/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type AuditCmd struct {
	Repair bool `cli:"repair" help:"Attempt to repair inconsistencies found in the store"`

	cli.Base
}

func (c *AuditCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak for audit: %w", err)
	}

	return cp.Audit(c.Repair)
}
