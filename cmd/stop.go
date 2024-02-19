package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewStopCommand() *cobra.Command {
	var branch string
	var release string
	var commit string
	var version string

	cmd := &cobra.Command{
		Use:   "stop <remote>",
		Short: "Stop a running cpak container",
		Long:  "Stop a running cpak container, closing all active processes.",
		Args:  cobra.MinimumNArgs(1),
		RunE:  StopContainer,
	}
	cmd.Flags().StringVarP(&version, "version", "v", "", "Specify a version")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "Specify a branch")
	cmd.Flags().StringVarP(&commit, "commit", "c", "", "Specify a commit")
	cmd.Flags().StringVarP(&release, "release", "r", "", "Specify a release")

	return cmd
}

func StopContainer(cmd *cobra.Command, args []string) (err error) {
	remote := args[0]

	version, _ := cmd.Flags().GetString("version")
	branch, _ := cmd.Flags().GetString("branch")
	commit, _ := cmd.Flags().GetString("commit")
	release, _ := cmd.Flags().GetString("release")

	fmt.Println("Stopping cpak from remote:", remote)

	cpak, err := cpak.NewCpak()
	if err != nil {
		return err
	}

	return cpak.Stop(remote, version, branch, commit, release)
}
