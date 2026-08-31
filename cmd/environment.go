/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

const environmentPolicySizeLimit = 1 << 20
const environmentApplicationExportSizeLimit = 2 << 20

type EnvironmentCmd struct {
	Action          string   `arg:"action" help:"Action: create, list, inspect, shell, stop, delete, policy, permissions, processes, signals, signal, application-exports, export-application or unexport-application"`
	Extra           []string `arg:"extra" help:"Arguments passed to the environment shell"`
	Environment     string   `cli:"environment,e" help:"Environment ID or name"`
	Name            string   `cli:"name,n" help:"Environment name"`
	Origin          string   `cli:"origin,o" help:"Installed package origin or alias"`
	Version         string   `cli:"version" help:"Installed package version"`
	Branch          string   `cli:"branch,b" help:"Installed package branch"`
	Commit          string   `cli:"commit,c" help:"Installed package commit"`
	Release         string   `cli:"release,r" help:"Installed package release"`
	Command         string   `cli:"command" default:"sh" help:"Shell command inside the environment"`
	Policy          string   `cli:"policy" help:"Policy JSON file, or - for standard input"`
	Application     string   `cli:"application" help:"Desktop application identifier inside the environment"`
	ApplicationData string   `cli:"application-data" help:"Application export JSON file, or - for standard input"`
	PID             int      `cli:"pid" help:"Process ID inside the environment"`
	Signal          string   `cli:"signal" default:"TERM" help:"Signal to send"`
	Terminal        bool     `cli:"terminal" help:"Run the shell in a terminal"`
	JSON            bool     `cli:"json,j" help:"Print output in JSON format"`
	Verbose         bool     `cli:"verbose,v" help:"Enable verbose output"`

	cli.Base
}

func (c *EnvironmentCmd) Run() error {
	if c.JSON {
		logger.MachineOutputMode()
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	switch strings.ToLower(c.Action) {
	case "create":
		if c.Name == "" || c.Origin == "" {
			return errors.New("create requires --name and --origin")
		}
		origin, err := resolveApplicationOrigin(cp, c.Origin)
		if err != nil {
			return err
		}
		environment, err := cp.CreateEnvironment(c.Name, origin, c.Version, c.Branch, c.Commit, c.Release)
		if err != nil {
			return err
		}
		return c.printEnvironment(environment)
	case "list":
		environments, err := cp.ListEnvironments()
		if err != nil {
			return err
		}
		if c.JSON {
			return printEnvironmentJSON(environments)
		}
		rows := make([][]string, 0, len(environments))
		for _, environment := range environments {
			rows = append(rows, []string{environment.Name, environment.Origin, environment.Version, environment.ID})
		}
		tools.ShowTable([]string{"Name", "Origin", "Version", "ID"}, rows)
		return nil
	case "inspect":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		return c.printEnvironment(environment)
	case "shell":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		command := c.Command
		if !strings.HasPrefix(command, "@") {
			command = "@" + command
		}
		arguments := c.Extra
		if len(arguments) == 0 {
			arguments = []string{"-i"}
		}
		cp.SetTerminalSession(c.Terminal)
		return cp.RunEnvironment(environment.ID, command, c.Verbose, arguments...)
	case "stop":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		return cp.StopEnvironment(environment.ID)
	case "delete", "remove":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		return cp.DeleteEnvironment(environment.ID)
	case "policy":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		if c.Policy == "" {
			return printEnvironmentJSON(environment.Policy)
		}
		policy, err := readEnvironmentPolicy(c.Policy)
		if err != nil {
			return err
		}
		environment, err = cp.SetEnvironmentPolicy(environment.ID, policy)
		if err != nil {
			return err
		}
		return c.printEnvironment(environment)
	case "permissions":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		permissions, err := cp.EnvironmentPermissionCeiling(environment.ID)
		if err != nil {
			return err
		}
		return printEnvironmentJSON(permissions)
	case "processes":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		processes, err := cp.EnvironmentProcesses(environment.ID)
		if err != nil {
			return err
		}
		if c.JSON {
			return printEnvironmentJSON(processes)
		}
		rows := make([][]string, 0, len(processes))
		for _, current := range processes {
			rows = append(rows, []string{strconv.Itoa(int(current.PID)), current.Command, fmt.Sprintf("%.1f%%", current.CPU), formatProcessMemory(current.Memory)})
		}
		tools.ShowTable([]string{"PID", "Command", "CPU", "Memory"}, rows)
		return nil
	case "signals":
		signals := cpak.EnvironmentSignalNames()
		if c.JSON {
			return printEnvironmentJSON(signals)
		}
		for _, signal := range signals {
			fmt.Println(signal)
		}
		return nil
	case "signal":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		if c.PID <= 0 {
			return errors.New("signal requires a positive --pid")
		}
		return cp.SignalEnvironmentProcess(environment.ID, c.PID, c.Signal)
	case "application-exports":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		applications, err := cp.ListEnvironmentApplicationExports(environment.ID)
		if err != nil {
			return err
		}
		return printEnvironmentJSON(applications)
	case "export-application":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		if c.Application == "" || c.ApplicationData == "" {
			return errors.New("export-application requires --application and --application-data")
		}
		export, err := readEnvironmentApplicationExport(c.ApplicationData)
		if err != nil {
			return err
		}
		state, err := cp.ExportEnvironmentApplication(environment.ID, c.Application, export)
		if err != nil {
			return err
		}
		return printEnvironmentJSON(state)
	case "unexport-application":
		environment, err := c.environment(cp)
		if err != nil {
			return err
		}
		if c.Application == "" {
			return errors.New("unexport-application requires --application")
		}
		state, err := cp.RemoveEnvironmentApplicationExport(environment.ID, c.Application)
		if err != nil {
			return err
		}
		return printEnvironmentJSON(state)
	default:
		return fmt.Errorf("unsupported environment action %q", c.Action)
	}
}

func readEnvironmentApplicationExport(path string) (types.EnvironmentApplicationExport, error) {
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return types.EnvironmentApplicationExport{}, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return types.EnvironmentApplicationExport{}, err
		}
		if !info.Mode().IsRegular() {
			return types.EnvironmentApplicationExport{}, errors.New("environment application export must be a regular file")
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, environmentApplicationExportSizeLimit+1))
	if err != nil {
		return types.EnvironmentApplicationExport{}, err
	}
	if len(data) > environmentApplicationExportSizeLimit {
		return types.EnvironmentApplicationExport{}, fmt.Errorf("environment application export exceeds %d bytes", environmentApplicationExportSizeLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	export := types.EnvironmentApplicationExport{}
	if err := decoder.Decode(&export); err != nil {
		return types.EnvironmentApplicationExport{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return types.EnvironmentApplicationExport{}, errors.New("environment application export contains multiple JSON values")
	}
	return export, nil
}

func (c *EnvironmentCmd) environment(cp cpak.Cpak) (types.Environment, error) {
	if c.Environment == "" {
		return types.Environment{}, errors.New("action requires --environment")
	}
	return cp.GetEnvironment(c.Environment)
}

func (c *EnvironmentCmd) printEnvironment(environment types.Environment) error {
	if c.JSON {
		return printEnvironmentJSON(environment)
	}
	c.Logger.Success("Environment %s (%s)", environment.Name, environment.ID)
	return nil
}

func printEnvironmentJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func readEnvironmentPolicy(path string) (types.Override, error) {
	var reader io.Reader
	var file *os.File
	if path == "-" {
		reader = os.Stdin
	} else {
		var err error
		file, err = os.Open(path)
		if err != nil {
			return types.Override{}, err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return types.Override{}, err
		}
		if !info.Mode().IsRegular() {
			return types.Override{}, errors.New("environment policy must be a regular file")
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, environmentPolicySizeLimit+1))
	if err != nil {
		return types.Override{}, err
	}
	if len(data) > environmentPolicySizeLimit {
		return types.Override{}, fmt.Errorf("environment policy exceeds %d bytes", environmentPolicySizeLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	policy := types.NewOverride()
	if err := decoder.Decode(&policy); err != nil {
		return types.Override{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return types.Override{}, errors.New("environment policy contains multiple JSON values")
	}
	return policy, nil
}

func formatProcessMemory(bytes uint64) string {
	const mebibyte = 1024 * 1024
	if bytes < mebibyte {
		return fmt.Sprintf("%d KiB", bytes/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(bytes)/mebibyte)
}
