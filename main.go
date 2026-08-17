/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/cmd"
	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/selfupdate"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type CLI struct {
	Install            cmd.InstallCmd            `cmd:"install" help:"Install a package from a remote Git repository"`
	Update             cmd.UpdateCmd             `cmd:"update" help:"Update one or all the packages in the local store"`
	Rollback           cmd.RollbackCmd           `cmd:"rollback" help:"Restore the previous installed version of a package"`
	Remove             cmd.RemoveCmd             `cmd:"remove" help:"Remove a package from the local store"`
	List               cmd.ListCmd               `cmd:"list" help:"List all the packages in the local store"`
	Shell              cmd.ShellCmd              `cmd:"shell" help:"Spawn a shell inside a container"`
	Run                cmd.RunCmd                `cmd:"run" help:"Run a package from a remote Git repository"`
	Logs               cmd.LogsCmd               `cmd:"logs" help:"Show output from a running application"`
	Orchestrate        cmd.OrchestrateCmd        `cmd:"orchestrate" help:"Run multiple cpak applications"`
	Spawn              cmd.SpawnCmd              `cmd:"spawn" help:"Spawn a container for a package"`
	Launch             cmd.LaunchCmd             `cmd:"launch" help:"Run a command inside an existing container sandbox"`
	ChromiumLaunch     cmd.ChromiumLaunchCmd     `cmd:"chromium-launch" help:"Launch or forward a Chromium command"`
	Service            cmd.ServiceCmd            `cmd:"service" help:"Manage cpak services"`
	Stop               cmd.StopCmd               `cmd:"stop" help:"Stop a running container"`
	Dedup              cmd.DedupCmd              `cmd:"dedup" help:"Deduplicate layers in the local store"`
	GC                 cmd.GCCmd                 `cmd:"gc" help:"Find or remove unreferenced data"`
	Audit              cmd.AuditCmd              `cmd:"audit" help:"Audit the local store for integrity"`
	Lock               cmd.LockCmd               `cmd:"lock" help:"Resolve a reproducible package lock file"`
	Dev                cmd.DevCmd                `cmd:"dev" help:"Install and launch a local package in isolation"`
	Test               cmd.TestCmd               `cmd:"test" help:"Validate a local package in isolation"`
	Alias              cmd.AliasCmd              `cmd:"alias" help:"Manage aliases for installed applications"`
	Override           cmd.OverrideCmd           `cmd:"override" help:"Manage application overrides"`
	Addon              cmd.AddonCmd              `cmd:"addon" help:"Manage application addons"`
	Extract            cmd.ExtractCmd            `cmd:"extract" help:"Extract a package to a local directory"`
	Init               cmd.InitCmd               `cmd:"init" help:"Initialize a new cpak package in the current directory"`
	GenSchema          cmd.GenSchemaCmd          `cmd:"gen-schema" help:"Generate the JSON schema for the cpak manifest"`
	Validate           cmd.ValidateCmd           `cmd:"validate" help:"Validate a cpak manifest"`
	Doctor             cmd.DoctorCmd             `cmd:"doctor" help:"Check host support for the cpak runtime"`
	MigrateManifest    cmd.MigrateManifestCmd    `cmd:"migrate-manifest" help:"Migrate a manifest to version 2"`
	SystemBrokerServer cmd.SystemBrokerServerCmd `cmd:"system-broker-server" help:"Start the system integration broker"`
	DesktopBusProxy    cmd.DesktopBusProxyCmd    `cmd:"desktop-bus-proxy" help:"Start the policy-gated desktop bus proxy"`
	HostAction         cmd.HostActionCmd         `cmd:"host-action" help:"Run a typed host action"`
	System             cmd.SystemCmd             `cmd:"system" help:"Manage privileged system integration"`
	SystemAuthority    cmd.SystemAuthorityCmd    `cmd:"system-authority" help:"Start the privileged system authority"`
	Session            cmd.SessionCmd            `cmd:"session" help:"Manage desktop and kiosk login sessions"`
	Auth               cmd.AuthCmd               `cmd:"auth" help:"Manage package registry access"`
	SelfUpdate         cmd.SelfUpdateCmd         `cmd:"self-update" help:"Update the cpak binary"`
	Storage            cmd.StorageCmd            `cmd:"storage" help:"Manage application storage"`
	Grant              cmd.GrantCmd              `cmd:"grant" help:"Manage persistent file grants"`

	cli.Base
}

var version = "0.0.1"
var selfUpdateMode = "enabled"

//go:embed cpak-icon.png
var cpakIcon []byte

func main() {
	desktopui.SetBrandIcon(cpakIcon)
	root := &CLI{}
	root.Run.Configure(cpakIcon)
	root.SelfUpdate.Configure(version, selfUpdateMode, cpakIcon)
	app, err := cli.New(root, cli.WithVersion(version))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	app.SetName("cpak")
	if err := app.Run(); err != nil {
		var exitErr *types.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func (c *CLI) Before() error {
	if skipUpdateCheck(os.Args) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	checker := selfupdate.Checker{CurrentVersion: version, Mode: selfUpdateMode}
	release, available, err := checker.Check(ctx, 24*time.Hour)
	if err != nil || !available {
		return nil
	}
	if startDesktopUpdate(checker, release.Version) {
		return nil
	}
	if selfUpdateMode == "managed" {
		fmt.Fprintf(os.Stderr, "cpak %s is available; ask your package maintainer to update it.\n", release.Version)
	} else {
		fmt.Fprintf(os.Stderr, "cpak %s is available. Run cpak self-update to install it.\n", release.Version)
	}
	return nil
}

func startDesktopUpdate(checker selfupdate.Checker, release string) bool {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" || checker.WasNotified(release) {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	command := exec.Command(executable, "self-update", "--desktop")
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = command.Start(); err != nil {
		return false
	}
	_ = command.Process.Release()
	if err = checker.MarkNotified(release); err != nil {
		return false
	}
	return true
}

func skipUpdateCheck(args []string) bool {
	if len(args) < 2 || strings.HasPrefix(version, "0.0.0-") || version == "0.0.1" || version == "dev" {
		return true
	}
	for _, argument := range args[1:] {
		if argument == "--version" || argument == "-v" || argument == "self-update" || argument == "system-broker-server" || argument == "desktop-bus-proxy" || argument == "system-authority" || argument == "spawn" || argument == "launch" || argument == "chromium-launch" || argument == "dedup" || argument == "host-action" {
			return true
		}
	}
	return false
}
