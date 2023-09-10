package cmd

/*
cpak shell <remote> <branch> (shell into a package)
*/

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewShellCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <remote> <branch>",
		Short: "Shell into a package",
		Args:  cobra.ExactArgs(2),
		RunE:  ShellPackage,
	}

	return cmd
}

func ShellPackage(cmd *cobra.Command, args []string) error {
	remote := args[0]
	branch := args[1]

	fmt.Printf("Shelling into package from remote %s, branch %s\n", remote, branch)
	return nil
}
