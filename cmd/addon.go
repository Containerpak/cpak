/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
	Action      string `arg:"action" help:"Action: list, slots, providers, use, enable or disable"`
	AppOrigin   string `arg:"app_origin" help:"Application origin"`
	AddonOrigin string `arg:"addon_origin" help:"Addon origin, slot or provider filter"`
	Provider    string `arg:"provider" help:"Provider origin or ID for use"`
	JSON        bool   `cli:"json,j" help:"Print list output as JSON"`
	Anyway      bool   `cli:"anyway" help:"Enable an addon the application does not offer, on your own responsibility"`

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
			rows = append(rows, []string{status.Origin, status.Slot, status.Provider, yesNo(status.Enabled), yesNo(status.Installed)})
		}
		tools.ShowTable([]string{"Addon", "Slot", "Provider", "Enabled", "Installed"}, rows)
		return nil
	case "slots":
		slots, slotErr := cp.AddonSlots(app)
		if slotErr != nil {
			return slotErr
		}
		if c.JSON {
			data, marshalErr := json.MarshalIndent(slots, "", "  ")
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}
		rows := make([][]string, 0, len(slots))
		for _, slot := range slots {
			rows = append(rows, []string{slot.Slot, slot.Mode, strings.Join(slot.Active, ", "), strings.Join(slot.Available, ", ")})
		}
		tools.ShowTable([]string{"Slot", "Mode", "Active", "Available"}, rows)
		return nil
	case "providers":
		statuses, statusErr := cp.AddonStatuses(app)
		if statusErr != nil {
			return statusErr
		}
		providers := make([]cpak.AddonStatus, 0, len(statuses))
		for _, status := range statuses {
			if status.Provider != "" && (c.AddonOrigin == "" || status.Slot == strings.ToLower(c.AddonOrigin)) {
				providers = append(providers, status)
			}
		}
		if c.JSON {
			data, marshalErr := json.MarshalIndent(providers, "", "  ")
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}
		rows := make([][]string, 0, len(providers))
		for _, provider := range providers {
			rows = append(rows, []string{provider.Slot, provider.Provider, provider.Origin, provider.Mode, yesNo(provider.Enabled)})
		}
		tools.ShowTable([]string{"Slot", "Provider", "Origin", "Mode", "Active"}, rows)
		return nil
	case "use":
		if c.AddonOrigin == "" || c.Provider == "" {
			return fmt.Errorf("slot and provider are required for use")
		}
		if err := cp.SelectAddonProvider(app, c.AddonOrigin, c.Provider); err != nil {
			return err
		}
		cp.EnrolApplication(app)
		c.Logger.Success("Provider %s selected for addon slot %s", c.Provider, c.AddonOrigin)
		return nil
	case "enable":
		if c.AddonOrigin == "" {
			return fmt.Errorf("addon origin is required for enable")
		}
		enable := cp.EnableAddon
		if c.Anyway {
			enable = cp.EnableChosenAddon
		}
		if err := enable(app, c.AddonOrigin); err != nil {
			return err
		}
		// An addon is part of the composed layers, so enabling one changes the
		// root a launch derives and the anchor has to follow it.
		cp.EnrolApplication(app)
		c.Logger.Success("Addon %s enabled for %s", c.AddonOrigin, c.AppOrigin)
		return nil
	case "disable":
		if c.AddonOrigin == "" {
			return fmt.Errorf("addon origin is required for disable")
		}
		if err := cp.DisableAddon(app, c.AddonOrigin); err != nil {
			return err
		}
		cp.EnrolApplication(app)
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
