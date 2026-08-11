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
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type AliasCmd struct {
	Action string `arg:"action" help:"Action: set, remove or list"`
	Name   string `arg:"name" help:"Alias name for set or remove"`
	Origin string `arg:"origin" help:"Installed application origin for set"`
	JSON   bool   `cli:"json,j" help:"Print list output in JSON format"`

	cli.Base
}

func (c *AliasCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	switch strings.ToLower(c.Action) {
	case "set":
		if c.Name == "" || c.Origin == "" {
			return fmt.Errorf("alias name and origin are required for set")
		}
		if err = cp.SetAlias(c.Name, c.Origin); err != nil {
			return err
		}
		c.Logger.Success("Alias %s saved", strings.ToLower(c.Name))
		return nil
	case "remove":
		if c.Name == "" {
			return fmt.Errorf("alias name is required for remove")
		}
		if err = cp.RemoveAlias(c.Name); err != nil {
			return err
		}
		c.Logger.Success("Alias %s removed", strings.ToLower(c.Name))
		return nil
	case "list":
		aliases, listErr := cp.ListAliases()
		if listErr != nil {
			return listErr
		}
		if c.JSON {
			data, marshalErr := json.MarshalIndent(aliases, "", "  ")
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}
		rows := make([][]string, 0, len(aliases))
		for _, alias := range aliases {
			rows = append(rows, []string{alias.Name, alias.Origin})
		}
		tools.ShowTable([]string{"Alias", "Origin"}, rows)
		return nil
	default:
		return fmt.Errorf("unsupported alias action %q", c.Action)
	}
}
