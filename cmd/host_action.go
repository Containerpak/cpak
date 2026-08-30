/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
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
	_, terminalErr := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	interactive := terminalErr == nil
	restoreTerminal, err := prepareHostActionTerminal(c.Shim, os.Stdin, interactive)
	if err != nil {
		return err
	}
	defer restoreTerminal()
	if err := systembroker.InvokeShim(ctx, socketPath, token, c.Shim, c.Args, environment, os.Stdin, os.Stdout, os.Stderr, interactive); err != nil {
		return fmt.Errorf("host action failed: %w", err)
	}
	return nil
}

func prepareHostActionTerminal(shim string, input *os.File, interactive bool) (func(), error) {
	if shim != "cpak-host" || !interactive {
		return func() {}, nil
	}
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return nil, fmt.Errorf("prepare host action terminal: %w", err)
	}
	return func() {
		_ = term.Restore(int(input.Fd()), state)
	}, nil
}
