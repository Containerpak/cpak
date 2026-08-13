/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type HostActionCmd struct {
	SocketPath string   `cli:"socket-path" help:"Path for the Unix domain socket"`
	TokenFile  string   `cli:"token-file" help:"File containing the broker token"`
	Shim       string   `cli:"shim" help:"Compatibility shim name"`
	Args       []string `arg:"args" help:"Shim arguments"`

	cli.Base
}

func (c *HostActionCmd) Run() error {
	socketPath := c.SocketPath
	if socketPath == "" {
		socketPath = os.Getenv("CPAK_SYSTEM_BROKER_SOCKET")
	}
	if socketPath == "" {
		return fmt.Errorf("system broker socket path is required")
	}
	tokenFile := c.TokenFile
	if tokenFile == "" {
		tokenFile = os.Getenv("CPAK_SYSTEM_BROKER_TOKEN_FILE")
	}
	token, err := readSystemBrokerToken(tokenFile)
	if err != nil {
		return err
	}
	environment := map[string]string{}
	for _, name := range []string{"WAYLAND_DISPLAY", "DISPLAY", "XDG_ACTIVATION_TOKEN"} {
		if value := os.Getenv(name); value != "" {
			environment[name] = value
		}
	}
	ctx, stop := signalContext()
	defer stop()
	if err := systembroker.InvokeShim(ctx, socketPath, token, c.Shim, c.Args, environment, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("host action failed: %w", err)
	}
	return nil
}
