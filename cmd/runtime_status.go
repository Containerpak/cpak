/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type PsCmd struct {
	JSON bool `cli:"json,j" help:"Print output in JSON format"`
	cli.Base
}

func (c *PsCmd) Run() error {
	statuses, err := loadRuntimeStatuses()
	if err != nil {
		return err
	}
	return printRuntimeStatuses(statuses, c.JSON)
}

type StatusCmd struct {
	Remote   string `arg:"remote" help:"Remote Git repository"`
	Instance string `cli:"instance,i" help:"Application instance or service name"`
	JSON     bool   `cli:"json,j" help:"Print output in JSON format"`
	cli.Base
}

func (c *StatusCmd) Run() error {
	statuses, origin, err := resolveRuntimeStatuses(c.Remote)
	if err != nil {
		return err
	}
	statuses, err = cpak.FilterRuntimeStatuses(statuses, origin, c.Instance)
	if err != nil {
		return err
	}
	return printRuntimeStatuses(statuses, c.JSON)
}

type InspectCmd struct {
	Remote   string `arg:"remote" help:"Remote Git repository"`
	Instance string `cli:"instance,i" help:"Application instance or service name"`
	cli.Base
}

func (c *InspectCmd) Run() error {
	statuses, origin, err := resolveRuntimeStatuses(c.Remote)
	if err != nil {
		return err
	}
	statuses, err = cpak.FilterRuntimeStatuses(statuses, origin, c.Instance)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(statuses, "", "  ")
	if err != nil {
		return err
	}
	if len(statuses) == 1 {
		encoded, err = json.MarshalIndent(statuses[0], "", "  ")
		if err != nil {
			return err
		}
	}
	fmt.Println(string(encoded))
	return nil
}

type HealthCmd struct {
	Remote   string `arg:"remote" help:"Remote Git repository"`
	Instance string `cli:"instance,i" help:"Application instance or service name"`
	JSON     bool   `cli:"json,j" help:"Print output in JSON format"`
	cli.Base
}

func (c *HealthCmd) Run() error {
	statuses, origin, err := resolveRuntimeStatuses(c.Remote)
	if err != nil {
		return err
	}
	statuses, err = cpak.FilterRuntimeStatuses(statuses, origin, c.Instance)
	if err != nil {
		return err
	}
	if err = printRuntimeStatuses(statuses, c.JSON); err != nil {
		return err
	}
	return cpak.RuntimeHealthError(statuses)
}

func loadRuntimeStatuses() ([]cpak.RuntimeStatus, error) {
	cp, err := cpak.NewCpak()
	if err != nil {
		return nil, err
	}
	return cp.RuntimeStatuses()
}

func resolveRuntimeStatuses(remote string) ([]cpak.RuntimeStatus, string, error) {
	cp, err := cpak.NewCpak()
	if err != nil {
		return nil, "", err
	}
	origin, err := resolveApplicationOrigin(cp, remote)
	if err != nil {
		return nil, "", err
	}
	statuses, err := cp.RuntimeStatuses()
	return statuses, origin, err
}

func printRuntimeStatuses(statuses []cpak.RuntimeStatus, jsonOutput bool) error {
	if jsonOutput {
		encoded, err := json.MarshalIndent(statuses, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	rows := make([][]string, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, []string{
			status.Package, displayInstance(status), status.ContainerState, status.ProcessState,
			status.Health, runtimeSince(status.Since), displayNetwork(status),
		})
	}
	tools.ShowTable([]string{"Package", "Instance", "Container", "Process", "Health", "Since", "Network"}, rows)
	return nil
}

func displayNetwork(status cpak.RuntimeStatus) string {
	if len(status.Ports) == 0 {
		return status.Network
	}
	ports := make([]string, 0, len(status.Ports))
	for _, port := range status.Ports {
		ports = append(ports, strconv.Itoa(port))
	}
	return status.Network + ":" + strings.Join(ports, ",")
}

func displayInstance(status cpak.RuntimeStatus) string {
	if status.Service != "" {
		return status.Service
	}
	if status.Instance == "" {
		return "default"
	}
	return status.Instance
}

func runtimeSince(since time.Time) string {
	if since.IsZero() {
		return "-"
	}
	duration := time.Since(since).Round(time.Second)
	if duration < 0 {
		return "0s"
	}
	return duration.String()
}
