/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type OrchestrateCmd struct {
	Remotes      []string `arg:"remotes" help:"Applications to start"`
	DependsOn    []string `cli:"depends-on" help:"Dependency rule in app=dependency1,dependency2 form"`
	Delay        int      `cli:"delay" default:"0" help:"Seconds to wait between starts"`
	Retries      int      `cli:"retries" default:"0" help:"Retries after a failed start"`
	Health       string   `cli:"health" help:"Command to run inside each application for health checks"`
	IgnoreErrors bool     `cli:"ignore-errors" help:"Continue when an application fails"`

	cli.Base
}

type orchestratedProcess struct {
	remote   string
	instance string
	command  *exec.Cmd
}

func (c *OrchestrateCmd) Run() error {
	if len(c.Remotes) == 0 {
		return fmt.Errorf("at least one application is required")
	}
	if c.Delay < 0 || c.Retries < 0 {
		return fmt.Errorf("delay and retries cannot be negative")
	}
	order, err := orchestrationOrder(c.Remotes, c.DependsOn)
	if err != nil {
		return err
	}
	cpakBinary, err := findCpakBinary()
	if err != nil {
		return err
	}
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}

	processes := make([]orchestratedProcess, 0, len(order))
	cleanup := func() {
		for i := len(processes) - 1; i >= 0; i-- {
			_ = cp.StopInstance(processes[i].remote, "", "", "", "", processes[i].instance)
			if processes[i].command.Process != nil {
				_ = processes[i].command.Process.Signal(syscall.SIGTERM)
			}
		}
	}

	for index, remote := range order {
		instance := fmt.Sprintf("orchestrated-%d", index+1)
		process, startErr := startOrchestrated(cpakBinary, remote, instance, c.Retries)
		if startErr != nil {
			cleanup()
			if c.IgnoreErrors {
				continue
			}
			return startErr
		}
		entry := orchestratedProcess{remote: remote, instance: instance, command: process}
		processes = append(processes, entry)
		if c.Health != "" {
			if err = checkHealth(cpakBinary, remote, instance, c.Health, c.Retries); err != nil {
				cleanup()
				if !c.IgnoreErrors {
					return err
				}
			}
		}
		if c.Delay > 0 && index < len(order)-1 {
			time.Sleep(time.Duration(c.Delay) * time.Second)
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	done := make(chan error, len(processes))
	for _, process := range processes {
		go func(process orchestratedProcess) { done <- process.command.Wait() }(process)
	}
	for range processes {
		select {
		case <-signals:
			cleanup()
			return nil
		case waitErr := <-done:
			if waitErr != nil && !c.IgnoreErrors {
				cleanup()
				return waitErr
			}
		}
	}
	return nil
}

func startOrchestrated(binary, remote, instance string, retries int) (*exec.Cmd, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		command := exec.Command(binary, "run", "--instance", instance, remote)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			lastErr = err
			continue
		}
		return command, nil
	}
	return nil, fmt.Errorf("failed to start %s: %w", remote, lastErr)
}

func checkHealth(binary, remote, instance, health string, retries int) error {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		command := exec.Command(binary, "run", "--instance", instance, remote, "@sh", "-c", health)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("health check failed for %s: %w", remote, lastErr)
}

func findCpakBinary() (string, error) {
	if filepath := os.Args[0]; strings.Contains(filepath, "/") {
		return filepath, nil
	}
	binary, err := exec.LookPath("cpak")
	if err != nil {
		return "", fmt.Errorf("cpak binary not found: %w", err)
	}
	return binary, nil
}

func orchestrationOrder(remotes, rules []string) ([]string, error) {
	dependencies := make(map[string][]string, len(rules))
	for _, rule := range rules {
		parts := strings.SplitN(rule, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, fmt.Errorf("invalid dependency rule %q", rule)
		}
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		for _, dependency := range strings.Split(parts[1], ",") {
			dependency = strings.ToLower(strings.TrimSpace(dependency))
			if dependency != "" {
				dependencies[name] = append(dependencies[name], dependency)
			}
		}
	}
	known := make(map[string]bool, len(remotes))
	for _, remote := range remotes {
		known[strings.ToLower(remote)] = true
	}
	for name, values := range dependencies {
		if !known[name] {
			return nil, fmt.Errorf("dependency rule references unknown application %s", name)
		}
		for _, dependency := range values {
			if !known[dependency] {
				return nil, fmt.Errorf("dependency %s references unknown application %s", name, dependency)
			}
		}
	}
	state := make(map[string]int, len(remotes))
	result := make([]string, 0, len(remotes))
	var visit func(string) error
	visit = func(remote string) error {
		key := strings.ToLower(remote)
		switch state[key] {
		case 1:
			return fmt.Errorf("dependency cycle detected at %s", remote)
		case 2:
			return nil
		}
		state[key] = 1
		for _, dependency := range dependencies[key] {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[key] = 2
		result = append(result, remote)
		return nil
	}
	for _, remote := range remotes {
		if err := visit(remote); err != nil {
			return nil, err
		}
	}
	return result, nil
}
