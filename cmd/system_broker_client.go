/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemBrokerClientCmd struct {
	SocketPath string   `cli:"socket-path" help:"Path for the Unix domain socket"`
	TokenFile  string   `cli:"token-file" help:"File containing the broker token"`
	Operation  string   `cli:"operation" help:"System integration operation"`
	Args       []string `arg:"args" help:"Operation arguments"`

	cli.Base
}

func (c *SystemBrokerClientCmd) Run() error {
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
	if err := systembroker.Call(socketPath, token, c.Operation, c.Args); err != nil {
		return fmt.Errorf("system broker request failed: %w", err)
	}
	return nil
}
