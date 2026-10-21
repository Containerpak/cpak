/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

// buildCapabilities names what this build can do, for programs that drive
// cpak. They must not infer it from the version, which is whatever the binary
// was built with. A name is only ever added: a program that finds it can rely
// on the behaviour it stands for.
var buildCapabilities = []string{
	"flatpak-install",
	"flatpak-update",
	"flatpak-migrate",
	"list-json",
}

type CapabilitiesCmd struct {
	JSON bool `cli:"json,j" help:"Print output in JSON format"`

	version string

	cli.Base
}

type capabilitiesReport struct {
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

func (c *CapabilitiesCmd) Configure(version string) {
	c.version = version
}

func (c *CapabilitiesCmd) Run() error {
	if !c.JSON {
		for _, capability := range buildCapabilities {
			fmt.Println(capability)
		}
		return nil
	}
	data, err := json.MarshalIndent(capabilitiesReport{Version: c.version, Capabilities: buildCapabilities}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
