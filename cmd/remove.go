package cmd

/*
cpak remove <remote> <version>
*/

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <remote> <version>",
		Short: "Remove a package installed from a remote Git repository",
		Args:  cobra.ExactArgs(2),
		RunE:  RemovePackage,
	}

	return cmd
}

func RemovePackage(cmd *cobra.Command, args []string) error {
	remote := args[0]
	branch := args[1]

	fmt.Printf("Removing package from remote %s, branch %s\n", remote, branch)
	return nil
}
