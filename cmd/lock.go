/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LockCmd struct {
	Manifest string `arg:"manifest" help:"Path to cpak.json"`
	Output   string `cli:"output,o" help:"Output path"`
	Origin   string `cli:"origin" help:"Repository origin used to resolve relative dependencies"`

	cli.Base
}

func (c *LockCmd) Run() error {
	manifestPath := c.Manifest
	if manifestPath == "" {
		manifestPath = "cpak.json"
	}
	manifest, err := loadManifest(manifestPath)
	if err != nil {
		return err
	}
	origin, err := resolveManifestOrigin(manifestPath, c.Origin, manifest)
	if err != nil {
		return err
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	lock, err := cp.BuildManifestLock(origin, manifest)
	if err != nil {
		return fmt.Errorf("create lock file: %w", err)
	}
	output := c.Output
	if output == "" {
		output = filepath.Join(filepath.Dir(manifestPath), "cpak.lock.json")
	}
	if err = writeJSONAtomic(output, lock); err != nil {
		return err
	}
	c.Logger.Success("Created %s.", output)
	return nil
}
