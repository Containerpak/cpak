/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mirkobrombin/cpak/pkg/appservice"
	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type ServiceCmd struct {
	Action string   `arg:"action" help:"Action: enable, disable, remove, start, stop, restart, list, status, logs, setup or restore"`
	Name   string   `arg:"name" help:"Service name"`
	Remote string   `arg:"remote" help:"Remote Git repository"`
	Binary string   `arg:"binary" help:"Binary to launch"`
	Extra  []string `arg:"extra" help:"Extra arguments for the binary"`

	Branch          string   `cli:"branch,b" help:"Specify a branch"`
	Commit          string   `cli:"commit,c" help:"Specify a commit"`
	Release         string   `cli:"release,r" help:"Specify a release"`
	ManifestService string   `cli:"service" help:"Use a service declared by the package"`
	DependsOn       []string `cli:"depends-on" help:"Wait for another cpak service"`
	Restart         string   `cli:"restart" default:"on-failure" help:"Restart policy: never, on-failure or always"`
	Health          string   `cli:"health" help:"Health command executed inside the application"`
	HealthDelay     int      `cli:"health-delay" default:"0" help:"Seconds before the first health check"`
	HealthInterval  int      `cli:"health-interval" default:"0" help:"Seconds between health checks"`
	HealthRetries   int      `cli:"health-retries" default:"0" help:"Health check retries before restart"`
	HealthTimeout   int      `cli:"health-timeout" default:"10" help:"Health check timeout in seconds"`
	Env             []string `cli:"env" help:"Environment entry in NAME=value form"`
	EnvFile         []string `cli:"env-file" help:"Environment file"`
	Secret          []string `cli:"secret" help:"Secret file in NAME=/absolute/path form"`
	Lines           int      `cli:"lines,n" default:"100" help:"Number of manager log lines"`

	cli.Base
}

func (c *ServiceCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	store := appservice.Store{Directory: filepath.Join(cp.Options.StorePath, "services")}
	switch strings.ToLower(c.Action) {
	case "", "restore":
		return c.runManager(cp, store)
	case "enable":
		return c.enable(cp, store)
	case "disable":
		return c.disable(cp, store)
	case "remove":
		return c.remove(cp, store)
	case "start", "stop", "restart":
		return c.control(cp, appservice.ControlRequest{Action: strings.ToLower(c.Action), Name: c.Name})
	case "list", "status":
		return c.show(cp, store)
	case "logs":
		return c.logs(store)
	case "setup":
		return c.setup(cp, store)
	default:
		return fmt.Errorf("unsupported service action %q", c.Action)
	}
}

func (c *ServiceCmd) enable(cp cpak.Cpak, store appservice.Store) error {
	if c.Name == "" {
		return errors.New("service name is required")
	}
	definition, existingErr := store.Get(c.Name)
	if c.Remote == "" {
		if existingErr != nil {
			return errors.New("remote repository is required for a new service")
		}
		definition.Enabled = true
	} else {
		origin, err := cp.ResolveInstalledOrigin(c.Remote)
		if err != nil {
			return err
		}
		if err = validateInstalledService(cp, origin, c.Branch, c.Commit, c.Release, c.ManifestService); err != nil {
			return err
		}
		definition = appservice.Definition{
			Name: c.Name, Origin: origin, Branch: c.Branch, Commit: c.Commit, Release: c.Release,
			ManifestService: c.ManifestService, Binary: c.Binary, Arguments: append([]string{}, c.Extra...),
			Environment: append([]string{}, c.Env...), EnvironmentFiles: append([]string{}, c.EnvFile...),
			Secrets: append([]string{}, c.Secret...), DependsOn: append([]string{}, c.DependsOn...),
			Restart: appservice.RestartPolicy(c.Restart), HealthCommand: c.Health,
			HealthDelay: c.HealthDelay, HealthInterval: c.HealthInterval,
			HealthRetries: c.HealthRetries, HealthTimeout: c.HealthTimeout, Enabled: true,
		}
	}
	if err := cp.ConfigureRuntime(definition.Environment, definition.EnvironmentFiles, definition.Secrets); err != nil {
		return err
	}
	previous := definition
	if existingErr == nil {
		previous, _ = store.Get(c.Name)
	}
	if err := store.Put(definition); err != nil {
		return err
	}
	if _, err := store.List(); err != nil {
		if existingErr == nil {
			_ = store.Put(previous)
		} else {
			_ = store.Delete(c.Name)
		}
		return err
	}
	if _, err := c.ensureBoot(store); err != nil {
		return err
	}
	if err := cp.EnsureService(); err != nil {
		return err
	}
	_, err := c.send(appservice.ControlRequest{Action: "reload"})
	if err == nil {
		c.Logger.Success("Service %s is enabled", c.Name)
	}
	return err
}

func validateInstalledService(cp cpak.Cpak, origin, branch, commit, release, serviceName string) error {
	store, err := cpak.NewStore(cp.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()
	app, err := store.GetApplicationByOrigin(origin, "", branch, commit, release)
	if err != nil {
		return err
	}
	if serviceName == "" {
		return nil
	}
	if _, found := app.ParsedServices[serviceName]; !found {
		return fmt.Errorf("application service %s is not declared by %s", serviceName, app.Name)
	}
	return nil
}

func (c *ServiceCmd) disable(cp cpak.Cpak, store appservice.Store) error {
	definition, err := store.Get(c.Name)
	if err != nil {
		return err
	}
	definition.Enabled = false
	if err = store.Put(definition); err != nil {
		return err
	}
	if err = cp.EnsureService(); err != nil {
		return err
	}
	_, err = c.send(appservice.ControlRequest{Action: "reload"})
	if err == nil {
		c.Logger.Success("Service %s is disabled", c.Name)
	}
	return err
}

func (c *ServiceCmd) remove(cp cpak.Cpak, store appservice.Store) error {
	if err := store.Remove(c.Name); err != nil {
		return err
	}
	if err := cp.EnsureService(); err != nil {
		return err
	}
	_, err := c.send(appservice.ControlRequest{Action: "reload"})
	if err == nil {
		c.Logger.Success("Service %s was removed", c.Name)
	}
	return err
}

func (c *ServiceCmd) control(cp cpak.Cpak, request appservice.ControlRequest) error {
	if c.Name == "" {
		return errors.New("service name is required")
	}
	if err := cp.EnsureService(); err != nil {
		return err
	}
	response, err := c.send(request)
	if err != nil {
		return err
	}
	return printServiceStatuses(response.Services, c.Name)
}

func (c *ServiceCmd) show(cp cpak.Cpak, store appservice.Store) error {
	if err := cp.EnsureService(); err == nil {
		if response, sendErr := c.send(appservice.ControlRequest{Action: "status"}); sendErr == nil {
			return printServiceStatuses(response.Services, c.Name)
		}
	}
	definitions, err := store.List()
	if err != nil {
		return err
	}
	statuses := make([]appservice.Status, 0, len(definitions))
	for _, definition := range definitions {
		statuses = append(statuses, appservice.Status{
			Name: definition.Name, Origin: definition.Origin, Instance: definition.Instance(),
			Enabled: definition.Enabled, State: "stopped", Health: "none",
		})
	}
	return printServiceStatuses(statuses, c.Name)
}

func (c *ServiceCmd) logs(store appservice.Store) error {
	if c.Name == "" {
		return errors.New("service name is required")
	}
	if c.Lines < 1 {
		return errors.New("lines must be greater than zero")
	}
	lines, err := appservice.ReadManagerLog(store, c.Name, c.Lines)
	if err != nil {
		return err
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

func (c *ServiceCmd) setup(cp cpak.Cpak, store appservice.Store) error {
	record, err := c.ensureBoot(store)
	if err != nil {
		return err
	}
	if err = cp.EnsureService(); err != nil {
		return err
	}
	c.Logger.Success("Boot activation uses %s", record.Adapter)
	return nil
}

func (c *ServiceCmd) ensureBoot(store appservice.Store) (appservice.BootRecord, error) {
	binary, err := findCpakBinary()
	if err != nil {
		return appservice.BootRecord{}, err
	}
	record, err := appservice.EnsureBoot(binary, store.Directory)
	if err != nil {
		return appservice.BootRecord{}, err
	}
	if record.Warning != "" {
		c.Logger.Warning(record.Warning)
	}
	return record, nil
}

func (c *ServiceCmd) runManager(cp cpak.Cpak, store appservice.Store) error {
	binary, err := findCpakBinary()
	if err != nil {
		return err
	}
	servicePath, err := cpak.HostServiceSocketPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	results := make(chan error, 2)
	go func() { results <- cp.StartSocketListenerContext(ctx) }()
	manager := appservice.Manager{
		Store: store, Binary: binary, SocketPath: appservice.ManagerSocketPath(servicePath),
	}
	go func() { results <- manager.Run(ctx) }()
	remaining := 2
	var result error
	for remaining > 0 {
		err = <-results
		remaining--
		if err != nil && result == nil {
			result = err
			stop()
		}
	}
	return result
}

func (c *ServiceCmd) send(request appservice.ControlRequest) (appservice.ControlResponse, error) {
	servicePath, err := cpak.HostServiceSocketPath()
	if err != nil {
		return appservice.ControlResponse{}, err
	}
	return appservice.Send(appservice.ManagerSocketPath(servicePath), request, 5*time.Second)
}

func printServiceStatuses(statuses []appservice.Status, name string) error {
	written := false
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "NAME\tENABLED\tSTATE\tHEALTH\tPID\tSINCE\tRESTARTS\tERROR")
	for _, status := range statuses {
		if name != "" && status.Name != name {
			continue
		}
		written = true
		since := "-"
		if !status.Since.IsZero() {
			since = time.Since(status.Since).Round(time.Second).String()
		}
		_, _ = fmt.Fprintf(writer, "%s\t%t\t%s\t%s\t%d\t%s\t%d\t%s\n", status.Name, status.Enabled, status.State, status.Health, status.PID, since, status.Restarts, status.LastError)
	}
	_ = writer.Flush()
	if name != "" && !written {
		return fmt.Errorf("service %s is not registered", name)
	}
	return nil
}
