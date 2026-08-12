/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"github.com/mirkobrombin/go-struct-flags/v1/binder"
)

type OverrideCmd struct {
	AppOrigin string `arg:"app_origin" help:"APP_ORIGIN"`
	Key       string `cli:"key,k" help:"Override key (required)"`
	Value     string `cli:"value,v" help:"Override value (required)"`

	cli.Base
}

func (c *OverrideCmd) Run() error {
	appOrigin := strings.ToLower(c.AppOrigin)

	if c.Key == "" || c.Value == "" {
		return fmt.Errorf("key and value are required")
	}

	// Initialize cpak and store
	cpk, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	store, err := cpak.NewStore(cpk.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()

	apps, err := store.GetApplications()
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("no cpak applications installed")
	}

	// Find the application by origin
	var sel types.Application
	for _, a := range apps {
		if a.Origin == appOrigin {
			sel = a
			break
		}
	}
	if sel.Origin == "" {
		return fmt.Errorf("application %q not found", appOrigin)
	}

	// Load existing override or fallback to manifest
	over := sel.ParsedOverride
	if userO, err := cpak.LoadOverride(appOrigin, sel.Version); err == nil {
		over = userO
	}
	if c.Key == "filesystem" {
		permissions, err := types.DecodeFilesystemPermissionsJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.Filesystem = permissions
		if err := cpak.SaveOverride(over, appOrigin, sel.Version); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}
	if c.Key == "hostActions" {
		actions, err := types.DecodeHostActionsJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.HostActions = actions
		if err := cpak.SaveOverride(over, appOrigin, sel.Version); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}

	// Initialize the flag binder
	b, err := binder.NewBinder(&over, os.TempDir(), true)
	if err != nil {
		return err
	}

	argsList := []string{c.Value}
	if c.Key == "fsExtra" || c.Key == "env" {
		argsList = strings.Split(c.Value, ":")
	}

	// Register the key with the binder
	if err := b.Run(c.Key, argsList); err != nil {
		return err
	}

	// Save the override
	if err := cpak.SaveOverride(over, appOrigin, sel.Version); err != nil {
		return err
	}

	c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
	return nil
}
