package cmd

/*
cpak remove <remote> <version>
*/

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewRemoveCommand() *cobra.Command {
	var branch string
	var release string
	var commit string

	cmd := &cobra.Command{
		Use:   "remove <remote>",
		Short: "Remove a package installed from a remote Git repository",
		Args:  cobra.ExactArgs(1),
		RunE:  RemovePackage,
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Specify a branch")
	cmd.Flags().StringVarP(&release, "release", "r", "", "Install a specific release")
	cmd.Flags().StringVarP(&commit, "commit", "c", "", "Specify a commit")

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
		fmt.Println("No version specified, using main branch if available")
		branch = "main"
	}

	err = cpak.Remove(remote, branch, release, commit)
	if err != nil {
		return fmt.Errorf("an error occurred while removing cpak: %s", err)
	}

	fmt.Printf("Cpak %s removed\n", remote)
	return nil
}
