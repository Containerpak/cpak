/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package main

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/cmd"
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
	rootCmd.AddCommand(cmd.NewShellCommand())
	rootCmd.AddCommand(cmd.NewRunCommand())
	rootCmd.AddCommand(cmd.NewSpawnCommand())
	rootCmd.AddCommand(cmd.NewServiceCommand())
	rootCmd.AddCommand(cmd.NewStopCommand())
	rootCmd.AddCommand(cmd.NewDedupCommand())
	rootCmd.AddCommand(cmd.NewAuditCommand())
	rootCmd.AddCommand(cmd.NewOverrideCommand())
	rootCmd.AddCommand(cmd.NewExtractCommand())
	rootCmd.AddCommand(cmd.NewInitCommand())
	rootCmd.AddCommand(cmd.NewHostExecServerCommand())
	rootCmd.AddCommand(cmd.NewHostExecClientCommand())

	rootCmd.Version = version
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
