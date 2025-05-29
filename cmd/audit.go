/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Audit the cpak store for integrity and optionally repair it",
		RunE:  runAudit,
	}
	cmd.Flags().Bool("repair", false, "Attempt to repair inconsistencies found in the store")
	return cmd
}

func runAudit(cmd *cobra.Command, args []string) error {
	repair, _ := cmd.Flags().GetBool("repair")

	c, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("failed to initialize cpak for audit: %w", err)
	}

	return c.Audit(repair)
}
