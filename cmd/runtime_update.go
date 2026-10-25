/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/mirkobrombin/cpak/pkg/systemauthority"
)

var installedRuntimeExecutable = systemauthority.InstalledExecutable
var runtimeNamespacesRestricted = systemauthority.AppArmorUserNamespacesRestricted

func prepareUpdatedRuntime(ctx context.Context, executable string) error {
	if systemauthority.Declarative() {
		return nil
	}
	installed, found := installedRuntimeExecutable()
	if !found && !runtimeNamespacesRestricted() {
		return nil
	}
	if found && systemauthority.SameExecutableContents(executable, installed) {
		return nil
	}
	command := exec.CommandContext(ctx, executable, "system", "setup")
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("prepare system runtime: %w", err)
	}
	return nil
}
