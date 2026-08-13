/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type ListCmd struct {
	JSON bool `cli:"json,j" help:"Print output in JSON format"`

	cli.Base
}

func (c *ListCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while listing cpak(s): %s", err)
	}

	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return fmt.Errorf("an error occurred while listing cpak(s): failed to open store: %w", err)
	}
	defer store.Close()

	apps, err := store.GetApplications()
	if err != nil {
		return fmt.Errorf("an error occurred while listing cpak(s): %s", err)
	}

	if !c.JSON {
		header := []string{"Name", "Version", "Timestamp", "Origin", "Source"}
		data := [][]string{}
		for _, app := range apps {
			data = append(data, []string{app.Name, app.Version, app.InstallTimestamp.Format(time.RFC3339), app.Origin, app.SourceType()})
		}
		tools.ShowTable(header, data)
	} else {
		jsonBytes, err := json.MarshalIndent(apps, "", "  ")
		if err != nil {
			return fmt.Errorf("an error occurred while listing cpak(s): %s", err)
		}
		fmt.Println(string(jsonBytes))
	}

	return nil
}
