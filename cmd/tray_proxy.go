/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type TrayProxyCmd struct {
	Display   string `cli:"display" help:"private X11 display socket"`
	Authority string `cli:"authority" help:"private X11 authority file"`
	Name      string `cli:"name" help:"fallback application title"`
	cli.Base
}

func (c *TrayProxyCmd) Run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return cpak.RunTrayProxy(ctx, c.Display, c.Authority, c.Name)
}
