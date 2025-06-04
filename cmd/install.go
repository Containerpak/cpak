/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/spf13/cobra"
)

func NewInstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install <remote>",
		Short: "Install a package from a remote Git repository",
		Args:  cobra.ExactArgs(1),
		RunE:  InstallPackage,
	}

	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("release", "r", "", "Install a specific release")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")

	return cmd
}

func installError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while installing cpak: %s", iErr)
	return
}

func InstallPackage(cmd *cobra.Command, args []string) (err error) {
	remote := strings.ToLower(args[0])

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

	manifest, err := cpak.FetchManifest(remote, branch, release, commit)
	if err != nil {
		return err
	}

	logger.Println("\nThe following cpak(s) will be installed:")
	logger.Printf("  - %s: %s", manifest.Name, manifest.Description)
	logger.Println()

	logger.Println("The following will be exported:")
	for _, binary := range manifest.Binaries {
		logger.Printf("  - (binary) %s", binary)
	}
	for _, dependency := range manifest.DesktopEntries {
		logger.Printf("  - (desktop entry) %s", dependency)
	}
	logger.Println()

	logger.Println("The following dependencies will be installed:")
	for _, dependency := range manifest.Dependencies {
		logger.Printf("  - %s", dependency)
	}
	logger.Println()

	logger.Println("The following permissions will be granted:")
	tools.PrintStructKeyVal(manifest.Override)
	logger.Println()

	confirm := tools.ConfirmOperation("Do you want to continue?")
	if !confirm {
		return
	}

	return cpak.InstallCpak(remote, manifest, branch, commit, release)
}
