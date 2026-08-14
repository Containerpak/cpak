/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemAuthorityCmd struct {
	cli.Base
}

func (c *SystemAuthorityCmd) Run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("system authority must run as root")
	}
	ctx, stop := signalContext()
	defer stop()
	return systemauthority.Serve(ctx)
}
