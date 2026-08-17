/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel); err != nil {
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
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel); err != nil {
			return err
		}
		c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
		return nil
	}
	if c.Key == "filePicker" {
		grant, err := types.DecodeFilePickerGrantJSON([]byte(c.Value))
		if err != nil {
			return err
		}
		over.FilePicker = grant
		if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel); err != nil {
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
	if err := saveOverrideAndEnrol(cpk, over, appOrigin, sel); err != nil {
		return err
	}

	c.Logger.Success("Override %s=%s saved for %s", c.Key, c.Value, appOrigin)
	return nil
}

// saveOverrideAndEnrol keeps the anchor in step with the policy. A saved
// override changes the root a launch derives, so without re-enrolling here the
// application keeps working exactly until the next launch, which then finds a
// root the ledger does not hold and refuses it at every enforcement level.
func saveOverrideAndEnrol(cpk cpak.Cpak, over types.Override, origin string, app types.Application) error {
	if err := cpak.SaveOverride(over, origin, app.Version); err != nil {
		return err
	}
	cpk.EnrolApplication(app)
	return nil
}
