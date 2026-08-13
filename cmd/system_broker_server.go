/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemBrokerServerCmd struct {
	SocketPath             string   `cli:"socket-path" help:"Path for the Unix domain socket"`
	TokenFile              string   `cli:"token-file" help:"File containing the broker token"`
	Notify                 bool     `cli:"notify" help:"Allow desktop notification requests"`
	OpenURI                bool     `cli:"open-uri" help:"Allow external URI requests"`
	HostApplications       string   `cli:"host-applications" help:"Host application catalog path"`
	DesktopRuntime         string   `cli:"desktop-runtime" help:"Host path for the nested desktop runtime"`
	ContainerOwner         string   `cli:"container-owner" help:"Identity which owns managed host containers"`
	ContainerCapability    []string `cli:"container-capability" help:"Allowed container provider capability"`
	ContainerPathReadOnly  []string `cli:"container-path-read-only" help:"Read-only source path for managed host containers"`
	ContainerPathReadWrite []string `cli:"container-path-read-write" help:"Read-write source path for managed host containers"`

	cli.Base
}

func (c *SystemBrokerServerCmd) Run() error {
	token, err := readSystemBrokerToken(c.TokenFile)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	var applications map[string]string
	if c.HostApplications != "" {
		applications, err = systembroker.LoadApplicationCatalog(c.HostApplications)
		if err != nil {
			return err
		}
	}
	containerCapabilities := map[string]bool{}
	for _, capability := range c.ContainerCapability {
		if capability != "read" && capability != "manage-owned" && capability != "exec-owned" {
			return fmt.Errorf("unsupported container capability: %s", capability)
		}
		containerCapabilities[capability] = true
	}
	containerPaths := make([]systembroker.ContainerPathGrant, 0, len(c.ContainerPathReadOnly)+len(c.ContainerPathReadWrite))
	for _, path := range c.ContainerPathReadOnly {
		containerPaths = append(containerPaths, systembroker.ContainerPathGrant{Path: path, ReadOnly: true})
	}
	for _, path := range c.ContainerPathReadWrite {
		containerPaths = append(containerPaths, systembroker.ContainerPathGrant{Path: path})
	}
	return systembroker.Serve(ctx, systembroker.Options{
		SocketPath:            c.SocketPath,
		Token:                 token,
		AllowNotify:           c.Notify,
		AllowOpenURI:          c.OpenURI,
		AllowHostApplications: c.HostApplications != "",
		Applications:          applications,
		RuntimeDirectory:      c.DesktopRuntime,
		ContainerOwner:        c.ContainerOwner,
		ContainerCapabilities: containerCapabilities,
		ContainerPaths:        containerPaths,
	})
}

func readSystemBrokerToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("system broker token file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read system broker token: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("system broker token file must not be accessible by other users")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read system broker token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", fmt.Errorf("system broker token file is invalid")
	}
	return token, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(signals)
	}()
	return ctx, cancel
}
