package main

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/cmd"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

var version = "0.0.1"

func main() {
	rootCmd := &cobra.Command{
		Use:   "cpak",
		Short: "package manager built around containers, Git, and OCI images",
		Long:  `cpak is a package manager built around containers, Git, and OCI images`,
	}

	rootCmd.AddCommand(cmd.NewInstallCommand())
	rootCmd.AddCommand(cmd.NewRemoveCommand())
	rootCmd.AddCommand(cmd.NewListCommand())
	rootCmd.AddCommand(cmd.NewInstallCommand())
	rootCmd.AddCommand(cmd.NewShellCommand())
	rootCmd.AddCommand(cmd.NewRunCommand())
	rootCmd.AddCommand(cmd.NewSpawnCommand())

	// test command
	rootCmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Test command",
		RunE: func(cmd *cobra.Command, args []string) error {
			cpak, err := cpak.NewCpak()
			if err != nil {
				return err
			}
			err = cpak.Install("https://github.com/mirkobrombin/cpak-test", "main", "", "")
			if err != nil {
				return err
			}
			_, err = cpak.Ce.Images(map[string][]string{})
			if err != nil {
				return err
			}
			return nil
		},
	})

	rootCmd.Version = version
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
