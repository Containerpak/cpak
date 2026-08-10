/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"os"

	hrun_client "github.com/containerpak/hrun/pkg/client"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type HostExecClientCmd struct {
	SocketPath string   `cli:"socket-path" help:"Path for the Unix domain socket"`
	Command    []string `arg:"command" help:"Command and arguments to execute"`

	cli.Base
}

func (c *HostExecClientCmd) Run() error {
	socketPath := c.SocketPath
	if socketPath == "" {
		socketPath = os.Getenv("CPAK_HOSTEXEC_SOCKET")
		if socketPath == "" {
			return fmt.Errorf("hostexec socket path not provided via --socket-path flag or CPAK_HOSTEXEC_SOCKET env var")
		}
	}

	c.Logger.Info("Starting hrun client for command %v on socket %s", c.Command, socketPath)
	err := hrun_client.StartClient(c.Command, socketPath)

	if err != nil {
		return fmt.Errorf("hrun client execution failed: %w", err)
	}

	c.Logger.Success("hrun client finished successfully.")
	return nil
}
