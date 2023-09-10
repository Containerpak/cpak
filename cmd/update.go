package cmd

/*
cpak update ?<remote> ?<branch> (update all remotes and branches if none specified, update is based on commit hash)
*/

import (
	"fmt"

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

	fmt.Printf("Updating packages. Remote: %s, Branch: %s\n", remote, branch)
	return nil
}
