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
	Portals          bool   `cli:"portals" help:"Serve the whole session bus and answer portals without a host bus"`
	AppName          string `cli:"app-name" help:"Application name used in notifications"`
	ColorScheme      int    `cli:"color-scheme" help:"Colour scheme reported by the settings portal"`
	Notifications    bool   `cli:"notifications" help:"Deliver notifications through the cpak broker"`
	OpenURI          bool   `cli:"open-uri" help:"Open links through the cpak broker"`
	ExecSocketPath   string `cli:"exec-socket" help:"Container command socket for processes started through the Flatpak portal"`
	IconTheme        string `cli:"icon-theme" help:"Icon theme reported by the settings portal"`

	cli.Base
}

func (c *DesktopBusProxyCmd) Run() error {
	policy, err := decodeDesktopBusPolicy(c.Policy)
	if err != nil {
		return err
	}
	token := ""
	if c.FilePicker || c.Portals && (c.Notifications || c.OpenURI) {
		token, err = readSystemBrokerToken(c.TokenFile)
		if err != nil {
			return err
		}
	}
	upstream := c.UpstreamAddress
	if upstream == "" && !c.Portals {
		if c.Bluetooth {
			upstream = "unix:path=/run/dbus/system_bus_socket"
		} else {
			upstream = os.Getenv("DBUS_SESSION_BUS_ADDRESS")
		}
	}
	if upstream == "" && !c.Portals {
		return errors.New("host session bus address is required")
	}
	if c.ColorScheme < 0 || c.ColorScheme > 2 {
		return errors.New("colour scheme must be 0, 1 or 2")
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
		Portals:          c.Portals,
		AppName:          c.AppName,
		ColorScheme:      uint32(c.ColorScheme),
		Notifications:    c.Notifications,
		OpenURI:          c.OpenURI,
		ExecSocketPath:   c.ExecSocketPath,
		IconTheme:        c.IconTheme,
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
