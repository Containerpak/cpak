/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/dabadee/v2/pkg/store"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DedupCmd struct {
	Verbose bool   `cli:"verbose,v" help:"enable verbose output"`
	Path    string `cli:"path" help:"the path to deduplicate"`

	cli.Base
}

func (c *DedupCmd) Run() error {
	c.Logger.Info("Deduplicating path %s in the DaBaDee storage..", c.Path)

	if c.Path == "" {
		err := fmt.Errorf("path is mandatory")
		c.Logger.Error(err.Error())
		return err
	}

	if c.Verbose {
		c.Logger.Info("Deduplicating path %s", c.Path)
	}

	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}

	s, err := store.Open(cp.Options.DaBaDeeStoreOptions)
	if err != nil {
		return err
	}
	defer s.Close()

	result, err := s.DeduplicateTree(context.Background(), c.Path)
	if err != nil {
		return err
	}

	c.Logger.Success("Deduplicated %d files", result.Files)
	return nil
}
