/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
