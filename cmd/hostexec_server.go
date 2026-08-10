/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	hrun_server "github.com/containerpak/hrun/pkg/server"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type HostExecServerCmd struct {
	SocketPath  string   `cli:"socket-path" help:"Path for the Unix domain socket"`
	AllowedCmds []string `cli:"allowed-cmd" help:"Allowed command to execute (can be specified multiple times)"`

	cli.Base
}

func (c *HostExecServerCmd) Run() error {
	if c.SocketPath == "" {
		return fmt.Errorf("socket-path is mandatory")
	}

	socketDir := filepath.Dir(c.SocketPath)
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return fmt.Errorf("failed to create socket directory %s: %w", socketDir, err)
	}

	_ = os.Remove(c.SocketPath)

	c.Logger.Info("Starting hrun server on socket: %s with allowed commands: %v", c.SocketPath, c.AllowedCmds)
	err := hrun_server.StartServer(c.AllowedCmds, c.SocketPath)

	if err != nil {
		c.Logger.Error("hrun server exited with error: %v", err)
		return fmt.Errorf("hostexec server failed: %w", err)
	}

	c.Logger.Success("hrun server finished successfully.")
	return nil
}
