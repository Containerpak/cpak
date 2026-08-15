/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"os"

	"github.com/mirkobrombin/cpak/pkg/desktopbus"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DesktopBusProxyCmd struct {
	SocketPath       string `cli:"socket-path" help:"Path for the private session bus socket"`
	UpstreamAddress  string `cli:"upstream-address" help:"Host session bus address"`
	BrokerSocketPath string `cli:"broker-socket-path" help:"Path for the cpak broker socket"`
	TokenFile        string `cli:"token-file" help:"File containing the broker token"`
	AllowSessionBus  bool   `cli:"allow-session-bus" help:"Forward the package session bus permission"`

	cli.Base
}

func (c *DesktopBusProxyCmd) Run() error {
	token, err := readSystemBrokerToken(c.TokenFile)
	if err != nil {
		return err
	}
	upstream := c.UpstreamAddress
	if upstream == "" {
		upstream = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	}
	if upstream == "" {
		return errors.New("host session bus address is required")
	}
	ctx, stop := signalContext()
	defer stop()
	return desktopbus.Serve(ctx, desktopbus.Options{
		SocketPath:       c.SocketPath,
		UpstreamAddress:  upstream,
		BrokerSocketPath: c.BrokerSocketPath,
		BrokerToken:      token,
		AllowSessionBus:  c.AllowSessionBus,
	})
}
