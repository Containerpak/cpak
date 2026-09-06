/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type CompletionCmd struct {
	Shell string `arg:"shell" required:"true" help:"Shell to generate completion for: bash, zsh or fish"`

	app    *cli.App  `internal:"ignore"`
	output io.Writer `internal:"ignore"`
	cli.Base
}

func (c *CompletionCmd) Configure(app *cli.App, output io.Writer) {
	c.app = app
	c.output = output
}

func (c *CompletionCmd) Run() error {
	output := c.output
	if output == nil {
		output = os.Stdout
	}

	switch c.Shell {
	case "bash":
		return c.app.GenBashCompletion(output)
	case "zsh":
		return c.app.GenZshCompletion(output)
	case "fish":
		return c.app.GenFishCompletion(output)
	default:
		return fmt.Errorf("unsupported shell: %s", c.Shell)
	}
}
