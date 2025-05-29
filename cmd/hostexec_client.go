/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"
	"log"
	"os"

	hrun_client "github.com/containerpak/hrun/pkg/client"
	"github.com/spf13/cobra"
)

func NewHostExecClientCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "hostexec-client -- [command] [args...]",
		Short:                 "Executes a command on the host via the hostexec server (internal)",
		RunE:                  runHostExecClient,
		Args:                  cobra.MinimumNArgs(1),
		DisableFlagsInUseLine: true,
		Hidden:                true,
	}

	cmd.Flags().String("socket-path", "", "Path for the Unix domain socket")

	return cmd
}

func runHostExecClient(cmd *cobra.Command, args []string) error {
	socketPath, _ := cmd.Flags().GetString("socket-path")
	if socketPath == "" {
		socketPath = os.Getenv("CPAK_HOSTEXEC_SOCKET")
		if socketPath == "" {
			return fmt.Errorf("hostexec socket path not provided via --socket-path flag or CPAK_HOSTEXEC_SOCKET env var")
		}
	}

	commandAndArgs := args

	log.Printf("Starting hrun client for command %v on socket %s", commandAndArgs, socketPath)
	err := hrun_client.StartClient(commandAndArgs, socketPath)

	if err != nil {
		return fmt.Errorf("hrun client execution failed: %w", err)
	}

	log.Println("hrun client finished successfully.")
	return nil
}
