/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type X11BrokerCmd struct {
	NestedDisplay      string `cli:"nested-display" help:"isolated X11 display"`
	NestedAuthority    string `cli:"nested-authority" help:"isolated X11 authority file"`
	HostDisplay        string `cli:"host-display" help:"host X11 display"`
	HostWindow         string `cli:"host-window" help:"nested server host window identity"`
	ServerPid          int    `cli:"server-pid" help:"nested X11 server process ID"`
	ServerStartTime    uint64 `cli:"server-start-time" help:"nested X11 server start time"`
	ContainerPid       int    `cli:"container-pid" help:"container process ID"`
	ContainerStartTime uint64 `cli:"container-start-time" help:"container process start time"`
	ContainerID        string `cli:"container-id" help:"container identity"`
	ReadyFD            int    `cli:"ready-fd" help:"readiness file descriptor"`
	HostToApp          bool   `cli:"host-to-app" help:"allow host clipboard reads"`
	AppToHost          bool   `cli:"app-to-host" help:"allow host clipboard writes"`

	cli.Base
}

func (c *X11BrokerCmd) Run() error {
	return cpak.RunX11Broker(cpak.X11BrokerOptions{
		NestedDisplay: c.NestedDisplay, NestedAuthority: c.NestedAuthority,
		HostDisplay: c.HostDisplay, HostWindow: c.HostWindow,
		ServerPid: c.ServerPid, ServerStartTime: c.ServerStartTime,
		ContainerPid: c.ContainerPid, ContainerStartTime: c.ContainerStartTime,
		ContainerID: c.ContainerID, ReadyFD: c.ReadyFD,
		HostToApp: c.HostToApp, AppToHost: c.AppToHost,
	})
}
