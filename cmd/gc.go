/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type GCCmd struct {
	Apply bool `cli:"apply" help:"Remove the reported data"`
	JSON  bool `cli:"json,j" help:"Print output in JSON format"`

	cli.Base
}

func (c *GCCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("open cpak store: %w", err)
	}
	report, err := cp.CollectGarbage(c.Apply)
	if err != nil {
		return fmt.Errorf("collect garbage: %w", err)
	}
	if c.JSON {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	rows := make([][]string, 0, len(report.Layers)+len(report.Cache))
	for _, item := range report.Layers {
		rows = append(rows, []string{"layer", item.Path, strconv.FormatInt(item.Bytes, 10)})
	}
	for _, item := range report.Cache {
		rows = append(rows, []string{"cache", item.Path, strconv.FormatInt(item.Bytes, 10)})
	}
	if report.FVSBlocks > 0 {
		rows = append(rows, []string{"content-store", "FVS", strconv.FormatInt(report.ObjectBytes-report.LegacyBytes, 10)})
	}
	if report.LegacyObjects > 0 || report.LegacyChunks > 0 {
		rows = append(rows, []string{"legacy-store", "DaBaDee", strconv.FormatInt(report.LegacyBytes, 10)})
	}
	if len(rows) == 0 {
		c.Logger.Success("No unused data found.")
		return nil
	}
	tools.ShowTable([]string{"Type", "Path", "Logical bytes"}, rows)
	if c.Apply {
		c.Logger.Success("Removed entries totaling %d logical bytes.", report.Bytes)
	} else {
		c.Logger.Info("Run cpak gc --apply to remove entries totaling %d logical bytes.", report.Bytes)
	}
	return nil
}
