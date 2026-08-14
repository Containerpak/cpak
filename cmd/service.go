/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
