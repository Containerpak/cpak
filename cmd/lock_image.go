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
	"runtime"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type LockImageCmd struct {
	Image        string   `arg:"image" help:"OCI image reference to resolve"`
	Packages     []string `cli:"nixery" help:"Nixery package names"`
	Registry     string   `cli:"registry" help:"Nixery registry hostname (default: nixery.dev)"`
	Architecture string   `cli:"architecture" help:"Target architecture (default: host architecture)"`
	cli.Base
}

func (c *LockImageCmd) Run() error {
	architecture := c.Architecture
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	image := c.Image
	if len(c.Packages) > 0 {
		if image != "" {
			return errors.New("choose an image reference or Nixery packages")
		}
		ref, err := oci.NixeryReference(c.Registry, c.Packages, architecture)
		if err != nil {
			return err
		}
		image = ref.Name()
	} else if c.Registry != "" {
		return errors.New("--registry requires --nixery")
	}
	if image == "" {
		return errors.New("an image reference or --nixery packages is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 5*time.Minute)
	defer timeout()
	resolved, err := (&oci.Client{}).Pin(ctx, image, oci.Platform{OS: "linux", Architecture: architecture})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, resolved)
	return err
}
