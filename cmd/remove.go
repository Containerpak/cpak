/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/spf13/cobra"
)

func NewRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <remote>",
		Short: "Remove a package installed from a remote Git repository",
		Args:  cobra.ExactArgs(1),
		RunE:  RemovePackage,
	}
	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("release", "r", "", "Install a specific release")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")

	return cmd
}

func RemovePackage(cmd *cobra.Command, args []string) error {
	remote := args[0]

	branch, _ := cmd.Flags().GetString("branch")
	release, _ := cmd.Flags().GetString("release")
	commit, _ := cmd.Flags().GetString("commit")

	cpak, err := cpak.NewCpak()
	if err != nil {
		return installError(err)
	}

	versionParams := []string{branch, release, commit}
	versionParamsCount := 0
	for _, versionParam := range versionParams {
		if versionParam != "" {
			versionParamsCount++
		}
	}
	// we can't specify more than one version parameter
	if versionParamsCount > 1 {
		return fmt.Errorf("more than one version parameter specified")
	}
	// if all version parameters are empty, we default to the main branch
	// assuming it is the default branch of the repository
	if versionParamsCount == 0 {
		logger.Println("No version specified, using main branch if available")
		branch = "main"
	}

	err = cpak.Remove(remote, branch, release, commit)
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
	}

	logger.Printf("Cpak %s removed", remote)
	return nil
}
