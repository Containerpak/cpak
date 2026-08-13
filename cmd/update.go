/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
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
	Remote         string `arg:"remote" help:"Remote Git repository, all the installed cpak(s) if omitted"`
	JSON           bool   `cli:"json,j" help:"Print output in JSON format"`
	NonInteractive bool   `cli:"non-interactive,n" help:"Reject updates that request additional permissions"`

	cli.Base
}

func (c *UpdateCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return fmt.Errorf("an error occurred while updating cpak(s): %s", err)
	}
	remote := ""
	if c.Remote != "" {
		remote, err = resolveApplicationOrigin(cp, c.Remote)
		if err != nil {
			return fmt.Errorf("an error occurred while updating cpak(s): %s", err)
		}
	}

	results, err := cp.UpdateWithOptions(remote, cpak.UpdateOptions{
		ConfirmPermissions: func(requests []types.UpdateResult) bool {
			if c.NonInteractive || c.JSON {
				return false
			}
			c.Logger.Info("The following updates request additional permissions:")
			data := make([][]string, 0, len(requests))
			for _, result := range requests {
				data = append(data, []string{result.Name, result.Origin, strings.Join(result.PermissionAdditions, ", ")})
			}
			tools.ShowTable([]string{"Name", "Origin", "Additional permissions"}, data)
			return tools.ConfirmOperation("Approve these permissions and continue?")
		},
	})
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

	header := []string{"Name", "Origin", "Source", "Status", "From", "To", "Permissions", "Details"}
	data := [][]string{}
	for _, result := range results {
		data = append(data, []string{
			result.Name,
			result.Origin,
			result.SourceType,
			string(result.Status),
			result.OldVersion,
			result.NewVersion,
			strings.Join(result.PermissionAdditions, ", "),
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
		if result.Status == types.UpdateStatusFailed || result.Status == types.UpdateStatusPermissionDenied {
			failed = append(failed, result.Origin)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("failed to update: %s", strings.Join(failed, ", "))
}
