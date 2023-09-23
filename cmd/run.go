package cmd

/*
cpak run <remote> --branch? --release? --commit?
*/

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewRunCommand() *cobra.Command {
	var version string
	var branch string
	var release string
	var commit string

	// we have to accept also unhandled flags which will be passed to the binary
	cmd := &cobra.Command{
		Use:   "run <remote>",
		Short: "Run a package from a remote Git repository",
		Long: `Run a package from a remote Git repository.

The binary to launch can be specified as a name or as a path. You can also
use the @ prefix to specify a binary that's not exported by the package.`,
		Args: cobra.MinimumNArgs(2),
		RunE: RunPackage,
	}
	cmd.Flags().StringVarP(&version, "version", "v", "", "Specify a version")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Specify a branch")
	cmd.Flags().StringVarP(&commit, "commit", "c", "", "Specify a commit")
	cmd.Flags().StringVarP(&release, "release", "r", "", "Specify a release")

	return cmd
}

func runError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while running cpak: %s", iErr)
	return
}

func RunPackage(cmd *cobra.Command, args []string) (err error) {
	remote := args[0]

	branch, _ := cmd.Flags().GetString("branch")
	commit, _ := cmd.Flags().GetString("commit")
	release, _ := cmd.Flags().GetString("release")

	binary := args[1]
	extraArgs := args[2:]

	fmt.Println("Running cpak from remote:", remote)

	version, _ := cmd.Flags().GetString("branch")

	cpak, err := cpak.NewCpak()
	if err != nil {
		return runError(err)
	}

	err = cpak.Run(remote, version, branch, commit, release, binary, extraArgs...)
	if err != nil {
		return runError(err)
	}

	fmt.Println("cpak ran successfully!")
	return nil
}
