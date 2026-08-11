/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type AddonCmd struct {
	Action      string `arg:"action" help:"Action: list, enable or disable"`
	AppOrigin   string `arg:"app_origin" help:"Application origin"`
	AddonOrigin string `arg:"addon_origin" help:"Addon origin for enable or disable"`
	JSON        bool   `cli:"json,j" help:"Print list output as JSON"`

	cli.Base
}

func (c *AddonCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	app, err := findAddonApplication(cp, strings.ToLower(c.AppOrigin))
	if err != nil {
		return err
	}

	switch strings.ToLower(c.Action) {
	case "list":
		statuses, statusErr := cp.AddonStatuses(app)
		if statusErr != nil {
			return statusErr
		}
		if c.JSON {
			data, marshalErr := json.MarshalIndent(statuses, "", "  ")
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}
		rows := make([][]string, 0, len(statuses))
		for _, status := range statuses {
			rows = append(rows, []string{status.Origin, yesNo(status.Enabled), yesNo(status.Installed)})
		}
		tools.ShowTable([]string{"Addon", "Enabled", "Installed"}, rows)
		return nil
	case "enable":
		if c.AddonOrigin == "" {
			return fmt.Errorf("addon origin is required for enable")
		}
		if err := cp.EnableAddon(app, c.AddonOrigin); err != nil {
			return err
		}
		c.Logger.Success("Addon %s enabled for %s", c.AddonOrigin, c.AppOrigin)
		return nil
	case "disable":
		if c.AddonOrigin == "" {
			return fmt.Errorf("addon origin is required for disable")
		}
		if err := cp.DisableAddon(app, c.AddonOrigin); err != nil {
			return err
		}
		c.Logger.Success("Addon %s disabled for %s", c.AddonOrigin, c.AppOrigin)
		return nil
	default:
		return fmt.Errorf("unsupported addon action %q", c.Action)
	}
}

func findAddonApplication(cp cpak.Cpak, origin string) (types.Application, error) {
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return types.Application{}, err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, "", "", "", "")
	if err != nil || app.CpakId == "" {
		return types.Application{}, fmt.Errorf("application %s is not installed", origin)
	}
	return app, nil
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
