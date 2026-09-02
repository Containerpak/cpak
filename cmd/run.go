/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	"golang.org/x/term"
)

type RunCmd struct {
	Remote string   `arg:"remote" help:"Remote Git repository"`
	Binary string   `arg:"binary" help:"Binary to launch"`
	Extra  []string `arg:"extra" help:"Extra arguments for the binary"`

	Verbose         bool     `cli:"verbose,v" help:"Enable verbose output"`
	DesktopLaunch   bool     `cli:"desktop-launch" help:"Grant files opened through a desktop launcher"`
	DesktopFileSpan string   `cli:"desktop-file-span" help:"How many launcher arguments precede and follow the selected files"`
	Instance        string   `cli:"instance,i" help:"Application instance"`
	Branch          string   `cli:"branch,b" help:"Specify a branch"`
	Commit          string   `cli:"commit,c" help:"Specify a commit"`
	Release         string   `cli:"release,r" help:"Specify a release"`
	Service         string   `cli:"service" help:"Run a service declared by the package"`
	Env             []string `cli:"env" help:"Set an environment entry in NAME=value form"`
	EnvFile         []string `cli:"env-file" help:"Load environment entries from an absolute path"`
	Secret          []string `cli:"secret" help:"Mount a private file at /run/secrets/NAME"`
	NestedRequest   string   `cli:"nested-request" help:"Run an encoded request from the cpak service"`
	icon            []byte

	cli.Base
}

func (c *RunCmd) Run() error {
	// From here on the standard output belongs to the program being run, and
	// an SDK shim is worthless the moment cpak writes a line into it.
	logger.ProxyMode()
	cp, err := cpak.NewCpak()
	if err != nil {
		return c.runError(err)
	}
	if c.NestedRequest != "" {
		params, decodeErr := cpak.DecodeNestedRequest(c.NestedRequest)
		if decodeErr != nil {
			return c.runError(decodeErr)
		}
		return c.runError(cp.RunAuthorized(params, c.Verbose))
	}
	c.configureStorageMigration(&cp)
	if c.Service != "" && c.Binary != "" {
		return c.runError(fmt.Errorf("service and binary are mutually exclusive"))
	}
	if err := cp.ConfigureRuntime(c.Env, c.EnvFile, c.Secret); err != nil {
		return c.runError(err)
	}
	if err := cp.SetApplicationService(c.Service); err != nil {
		return c.runError(err)
	}
	cp.SetDesktopLaunch(c.DesktopLaunch)
	if err := cp.SetDesktopFileSpan(c.DesktopFileSpan); err != nil {
		return c.runError(err)
	}

	remote, err := resolveRunOrigin(cp, c.Remote)
	if err != nil {
		return c.runError(err)
	}
	logger.Println("Running cpak from remote:", remote)

	err = cp.RunInstance(remote, "", c.Branch, c.Commit, c.Release, c.Instance, c.Binary, c.Verbose, c.Extra...)
	if err != nil {
		return c.runError(err)
	}

	return nil
}

func (c *RunCmd) Configure(icon []byte) {
	c.icon = icon
}

func (c *RunCmd) configureStorageMigration(cp *cpak.Cpak) {
	cp.SetStorageMigrationHandler(func(run func(func(cpak.StorageMigrationProgress)) error) error {
		if term.IsTerminal(int(os.Stdout.Fd())) || term.IsTerminal(int(os.Stderr.Fd())) {
			lastLayer := 0
			lastPercentage := -5
			return run(func(report cpak.StorageMigrationProgress) {
				percentage := migrationPercentage(report)
				if report.Layer == lastLayer && percentage < lastPercentage+5 && report.Bytes < report.TotalBytes {
					return
				}
				lastLayer = report.Layer
				lastPercentage = percentage
				logger.Printf("Migrating storage: layer %d of %d, %s of %s", report.Layer, report.Layers, formatMigrationBytes(report.Bytes), formatMigrationBytes(report.TotalBytes))
			})
		}
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return run(nil)
		}
		request := desktopui.ProgressRequest{
			Title: "cpak storage upgrade", Heading: "Updating application storage",
			Detail: "Converting existing layers to the current cpak storage format", IconPNG: c.icon,
		}
		return desktopui.Progress(desktopui.SelectBackend(""), request, func(progress func(desktopui.ProgressUpdate)) error {
			return run(func(report cpak.StorageMigrationProgress) {
				progress(desktopui.ProgressUpdate{
					Message: fmt.Sprintf("Layer %d of %d, %s of %s", report.Layer, report.Layers, formatMigrationBytes(report.Bytes), formatMigrationBytes(report.TotalBytes)),
					Current: report.Bytes, Total: report.TotalBytes,
				})
			})
		})
	})
	cp.SetStoragePreparationHandler(func(run func() error) error {
		if term.IsTerminal(int(os.Stdout.Fd())) || term.IsTerminal(int(os.Stderr.Fd())) {
			done := make(chan struct{})
			go func() {
				select {
				case <-time.After(400 * time.Millisecond):
					logger.Println("Preparing application storage...")
				case <-done:
				}
			}()
			err := run()
			close(done)
			return err
		}
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return run()
		}
		request := desktopui.ProgressRequest{
			Title: "cpak storage upgrade", Heading: "Preparing application storage",
			Detail: "Building the native application view", IconPNG: c.icon,
		}
		return desktopui.Progress(desktopui.SelectBackend(""), request, func(progress func(desktopui.ProgressUpdate)) error {
			progress(desktopui.ProgressUpdate{Message: "Preparing application files"})
			return run()
		})
	})
}

func migrationPercentage(report cpak.StorageMigrationProgress) int {
	if report.TotalBytes <= 0 {
		return 0
	}
	return int(report.Bytes * 100 / report.TotalBytes)
}

func formatMigrationBytes(value int64) string {
	const unit = int64(1024)
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	labels := []string{"KiB", "MiB", "GiB", "TiB"}
	scaled := float64(value)
	index := -1
	for scaled >= float64(unit) && index < len(labels)-1 {
		scaled /= float64(unit)
		index++
	}
	return fmt.Sprintf("%.1f %s", scaled, labels[index])
}

func (c *RunCmd) runError(iErr error) error {
	if iErr == nil {
		return nil
	}
	return fmt.Errorf("an error occurred while running cpak: %w", iErr)
}
