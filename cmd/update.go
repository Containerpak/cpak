/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

// TODO: implement the update command

import (
	"github.com/mirkobrombin/cpak/pkg/logger"

	"github.com/spf13/cobra"
)

func UpdatePackages(cmd *cobra.Command, args []string) error {
	remote := ""
	branch := ""

	if len(args) >= 1 {
		remote = args[0]
	}

	if len(args) == 2 {
		branch = args[1]
	}

	logger.Printf("Updating packages. Remote: %s, Branch: %s", remote, branch)
	return nil
}
