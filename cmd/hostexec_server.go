/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	hrun_server "github.com/containerpak/hrun/pkg/server"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/spf13/cobra"
)

func NewHostExecServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hostexec-server",
		Short:  "Starts the host execution server for a container (internal)",
		RunE:   runHostExecServer,
		Hidden: true,
	}

	cmd.Flags().String("socket-path", "", "Path for the Unix domain socket")
	cmd.Flags().StringArray("allowed-cmd", []string{}, "Allowed command to execute (can be specified multiple times)")
	cmd.MarkFlagRequired("socket-path")

	return cmd
}

func runHostExecServer(cmd *cobra.Command, args []string) error {
	socketPath, _ := cmd.Flags().GetString("socket-path")
	allowedCmds, _ := cmd.Flags().GetStringArray("allowed-cmd")

	socketDir := filepath.Dir(socketPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", socketDir, err)
	}

	_ = os.Remove(socketPath)

	logger.Printf("Starting hrun server on socket: %s with allowed commands: %v", socketPath, allowedCmds)
	err := hrun_server.StartServer(allowedCmds, socketPath)

	if err != nil {
		logger.Printf("hrun server exited with error: %v", err)
		return fmt.Errorf("hostexec server failed: %w", err)
	}

	logger.Println("hrun server finished successfully.")
	return nil
}
