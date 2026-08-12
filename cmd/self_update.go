/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/selfupdate"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SelfUpdateCmd struct {
	Check   bool `cli:"check" help:"Check for an update without installing it"`
	Desktop bool `cli:"desktop" help:"Show the desktop update interface"`

	version string
	mode    string
	icon    []byte

	cli.Base
}

func (c *SelfUpdateCmd) Run() error {
	checker := selfupdate.Checker{CurrentVersion: c.version, Mode: c.mode}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release, available, err := checker.Check(ctx, 0)
	if err != nil {
		return err
	}
	if !available {
		c.Logger.Success("cpak %s is up to date", c.version)
		return nil
	}
	if c.Check {
		c.Logger.Info("cpak %s is available", release.Version)
		return nil
	}
	if c.Desktop {
		request := desktopui.UpdateRequest{
			CurrentVersion: c.version,
			Version:        release.Version,
			Notes:          release.Notes,
			Managed:        c.mode == "managed",
			IconPNG:        c.icon,
		}
		return desktopui.Update(desktopui.SelectBackend(""), request, func(progress func(string)) error {
			progress("Downloading cpak")
			return checker.Install(ctx, release)
		})
	}
	if err = checker.Install(ctx, release); err != nil {
		if errors.Is(err, selfupdate.ErrManagedInstall) {
			return fmt.Errorf("cpak %s is available; ask your package maintainer to update it", release.Version)
		}
		return err
	}
	c.Logger.Success("cpak %s installed", release.Version)
	return nil
}

func (c *SelfUpdateCmd) Configure(version, mode string, icon []byte) {
	c.version = version
	c.mode = mode
	c.icon = icon
}
