/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type ServiceCmd struct {
	cli.Base
}

func (c *ServiceCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		c.Logger.Error("cpak service exited with error! %v", err)
		return fmt.Errorf("an error occurred while starting the cpak service: %s", err)
	}

	err = cp.StartSocketListener()
	if err != nil {
		c.Logger.Error("cpak service exited with error! %v", err)
		return fmt.Errorf("an error occurred while starting the cpak service: %s", err)
	}

	c.Logger.Success("cpak service exited successfully!")
	return nil
}
