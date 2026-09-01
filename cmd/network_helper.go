/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type NetworkHelperCmd struct {
	SlirpPath    string `cli:"slirp-path" help:"path to slirp4netns"`
	NamespacePid int    `cli:"namespace-pid" help:"network namespace process ID"`
	ReadyFd      int    `cli:"ready-fd" help:"readiness file descriptor"`
	ExitFd       int    `cli:"exit-fd" help:"container lifecycle file descriptor"`

	cli.Base
}

func (c *NetworkHelperCmd) Run() error {
	return cpak.RunUserNetworkHelper(c.SlirpPath, c.NamespacePid, c.ReadyFd, c.ExitFd)
}
