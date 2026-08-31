/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/mirkobrombin/cpak/pkg/desktopbus"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DesktopBusProxyCmd struct {
	SocketPath       string `cli:"socket-path" help:"Path for the private session bus socket"`
	UpstreamAddress  string `cli:"upstream-address" help:"Host session bus address"`
	BrokerSocketPath string `cli:"broker-socket-path" help:"Path for the cpak broker socket"`
	TokenFile        string `cli:"token-file" help:"File containing the broker token"`
	FilePicker       bool   `cli:"file-picker" help:"Enable the native file chooser broker"`
	NetworkMonitor   bool   `cli:"network-monitor" help:"Expose the desktop portal network monitor"`
	Bluetooth        bool   `cli:"bluetooth" help:"Expose only BlueZ on a private system bus socket"`
	Policy           string `cli:"policy" help:"Encoded filtered session bus policy"`

	cli.Base
}

func (c *DesktopBusProxyCmd) Run() error {
	policy, err := decodeDesktopBusPolicy(c.Policy)
	if err != nil {
		return err
	}
	token := ""
	if c.FilePicker {
		token, err = readSystemBrokerToken(c.TokenFile)
		if err != nil {
			return err
		}
	}
	upstream := c.UpstreamAddress
	if upstream == "" {
		if c.Bluetooth {
			upstream = "unix:path=/run/dbus/system_bus_socket"
		} else {
			upstream = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
		}
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
		FilePicker:       c.FilePicker,
		NetworkMonitor:   c.NetworkMonitor,
		Bluetooth:        c.Bluetooth,
		Policy:           policy,
	})
}

func decodeDesktopBusPolicy(value string) (types.DBusPolicy, error) {
	if value == "" {
		return types.DBusPolicy{}, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return types.DBusPolicy{}, err
	}
	var policy types.DBusPolicy
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&policy); err != nil {
		return types.DBusPolicy{}, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return types.DBusPolicy{}, errors.New("session bus policy contains trailing data")
	}
	if err = types.ValidateDBusPolicy(policy); err != nil {
		return types.DBusPolicy{}, err
	}
	return policy, nil
}
