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
	"github.com/spf13/cobra"
)

func NewShellCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <remote>",
		Short: "Shell into a cpak",
		Args:  cobra.MinimumNArgs(1),
		RunE:  ShellPackage,
	}
	cmd.Flags().BoolP("verbose", "v", false, "Enable verbose output")
	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")
	cmd.Flags().StringP("release", "r", "", "Specify a release")

	return cmd
}

func shellError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while opening the cpak shell: %s", iErr)
	return
}

func ShellPackage(cmd *cobra.Command, args []string) (err error) {
	remote := strings.ToLower(args[0])

	verbose, _ := cmd.Flags().GetBool("verbose")
	branch, _ := cmd.Flags().GetString("branch")
	commit, _ := cmd.Flags().GetString("commit")
	release, _ := cmd.Flags().GetString("release")

	binary := "@sh"

	fmt.Println("Running cpak from remote:", remote)

	version, _ := cmd.Flags().GetString("branch")

	cpak, err := cpak.NewCpak()
	if err != nil {
		return shellError(err)
	}

	err = cpak.Run(remote, version, branch, commit, release, binary, verbose, "-i")
	if err != nil {
		return shellError(err)
	}

	return nil
}
