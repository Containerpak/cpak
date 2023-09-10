package cmd

/*
cpak install <remote> --branch? --release? --commit?
*/

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewInstallCommand() *cobra.Command {
	var branch string
	var release string
	var commit string

	cmd := &cobra.Command{
		Use:   "install <remote>",
		Short: "Install a package from a remote Git repository",
		Args:  cobra.ExactArgs(1),
		RunE:  InstallPackage,
	}

	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Specify a branch")
	cmd.Flags().StringVarP(&release, "release", "r", "", "Install a specific release")
	cmd.Flags().StringVarP(&commit, "commit", "c", "", "Specify a commit")

	return cmd
}

func installError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while installing cpak: %s", iErr)
	return
}

func InstallPackage(cmd *cobra.Command, args []string) (err error) {
	remote := args[0]
	fmt.Println("Installing cpak from remote:", remote)

	branch, _ := cmd.Flags().GetString("branch")
	release, _ := cmd.Flags().GetString("release")
	commit, _ := cmd.Flags().GetString("commit")

	cpak, err := cpak.NewCpak()
	if err != nil {
		return installError(err)
	}

	err = cpak.Install(remote, branch, release, commit)
	if err != nil {
		return installError(err)
	}

	fmt.Println("cpak installed successfully!")
	return nil
}
