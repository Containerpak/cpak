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

type UpdateCmd struct {
	Remote string `arg:"remote" help:"Remote Git repository, all the installed cpak(s) if omitted"`
	JSON   bool   `cli:"json,j" help:"Print output in JSON format"`

	cli.Base
}

func (c *UpdateCmd) Run() error {
	remote := strings.ToLower(c.Remote)

	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while updating cpak(s): %s", err)
	}

	results, err := cp.Update(remote)
	if err != nil {
		return fmt.Errorf("an error occurred while updating cpak(s): %s", err)
	}

	if c.JSON {
		jsonBytes, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return fmt.Errorf("an error occurred while updating cpak(s): %s", err)
		}
		fmt.Println(string(jsonBytes))
		return updateFailures(results)
	}

	if len(results) == 0 {
		c.Logger.Info("No cpak installed, nothing to update")
		return nil
	}

	header := []string{"Name", "Origin", "Source", "Status", "From", "To", "Details"}
	data := [][]string{}
	for _, result := range results {
		data = append(data, []string{
			result.Name,
			result.Origin,
			result.SourceType,
			string(result.Status),
			result.OldVersion,
			result.NewVersion,
			result.Reason,
		})
	}
	tools.ShowTable(header, data)

	return updateFailures(results)
}

// updateFailures returns an error when at least one application could not be
// updated, so that the command exits with a failure.
func updateFailures(results []types.UpdateResult) error {
	failed := []string{}
	for _, result := range results {
		if result.Status == types.UpdateStatusFailed {
			failed = append(failed, result.Origin)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("failed to update: %s", strings.Join(failed, ", "))
}
