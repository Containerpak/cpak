package cmd

import (
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <remote>",
		Short: "Run a package from a remote Git repository",
		Long: `Run a package from a remote Git repository.

The binary to launch can be specified as a name or as a path. You can also
use the @ prefix to specify a binary that's not exported by the package.`,
		Args: cobra.MinimumNArgs(2),
		RunE: RunPackage,
	}
	cmd.Flags().BoolP("verbose", "v", false, "Enable verbose output")
	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")
	cmd.Flags().StringP("release", "r", "", "Specify a release")

	return cmd
}

func runError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while running cpak: %s", iErr)
	return
}

func RunPackage(cmd *cobra.Command, args []string) (err error) {
	remote := strings.ToLower(args[0])

	verbose, _ := cmd.Flags().GetBool("verbose")
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

	err = cpak.Run(remote, version, branch, commit, release, binary, verbose, extraArgs...)
	if err != nil {
		return runError(err)
	}

	return nil
}
