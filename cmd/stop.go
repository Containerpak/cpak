package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewStopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <remote>",
		Short: "Stop a running cpak container",
		Long:  "Stop a running cpak container, closing all active processes.",
		Args:  cobra.MinimumNArgs(1),
		RunE:  StopContainer,
	}
	cmd.Flags().StringP("version", "v", "", "Specify a version")
	cmd.Flags().StringP("branch", "b", "", "Specify a branch")
	cmd.Flags().StringP("commit", "c", "", "Specify a commit")
	cmd.Flags().StringP("release", "r", "", "Specify a release")

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

	err = cpak.Stop(remote, version, branch, commit, release)
	if err != nil {
		return fmt.Errorf("an error occurred while stopping the cpak container: %s", err)
	}

	return nil
}
