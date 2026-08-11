/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type DoctorCmd struct {
	JSON bool `cli:"json" help:"Print the report as JSON"`

	cli.Base
}

func (c *DoctorCmd) Run() error {
	report := cpak.Doctor()
	if c.JSON {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		for _, check := range report.Checks {
			status := "WARN"
			if check.Available {
				status = "OK"
			} else if check.Required {
				status = "FAIL"
			}
			fmt.Printf("[%s] %s: %s\n", status, check.Name, check.Detail)
		}
	}
	if !report.Ready {
		return fmt.Errorf("cpak runtime requirements are not satisfied")
	}
	return nil
}
