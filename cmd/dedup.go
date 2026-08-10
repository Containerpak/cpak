/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/dabadee/pkg/dabadee"
	"github.com/mirkobrombin/dabadee/pkg/hash"
	"github.com/mirkobrombin/dabadee/pkg/processor"
	"github.com/mirkobrombin/dabadee/pkg/storage"
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

	s, err := storage.NewStorage(cp.Options.DaBaDeeStoreOptions)
	if err != nil {
		return err
	}

	h := hash.NewSHA256Generator()
	p := processor.NewDedupProcessor(c.Path, "", s, h, 2)

	d := dabadee.NewDaBaDee(p, c.Verbose)
	err = d.Run()
	if err != nil {
		return err
	}

	c.Logger.Success("Deduplication completed successfully")
	return nil
}
