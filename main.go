/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/cmd"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type CLI struct {
	Install         cmd.InstallCmd         `cmd:"install" help:"Install a package from a remote Git repository"`
	Update          cmd.UpdateCmd          `cmd:"update" help:"Update one or all the packages in the local store"`
	Remove          cmd.RemoveCmd          `cmd:"remove" help:"Remove a package from the local store"`
	List            cmd.ListCmd            `cmd:"list" help:"List all the packages in the local store"`
	Shell           cmd.ShellCmd           `cmd:"shell" help:"Spawn a shell inside a container"`
	Run             cmd.RunCmd             `cmd:"run" help:"Run a package from a remote Git repository"`
	Logs            cmd.LogsCmd            `cmd:"logs" help:"Show output from a running application"`
	Orchestrate     cmd.OrchestrateCmd     `cmd:"orchestrate" help:"Run multiple cpak applications"`
	Spawn           cmd.SpawnCmd           `cmd:"spawn" help:"Spawn a container for a package"`
	Launch          cmd.LaunchCmd          `cmd:"launch" help:"Launch a command inside a package container"`
	Service         cmd.ServiceCmd         `cmd:"service" help:"Manage cpak services"`
	Stop            cmd.StopCmd            `cmd:"stop" help:"Stop a running container"`
	Dedup           cmd.DedupCmd           `cmd:"dedup" help:"Deduplicate layers in the local store"`
	Audit           cmd.AuditCmd           `cmd:"audit" help:"Audit the local store for integrity"`
	Override        cmd.OverrideCmd        `cmd:"override" help:"Manage application overrides"`
	Addon           cmd.AddonCmd           `cmd:"addon" help:"Manage application addons"`
	Extract         cmd.ExtractCmd         `cmd:"extract" help:"Extract a package to a local directory"`
	Init            cmd.InitCmd            `cmd:"init" help:"Initialize a new cpak package in the current directory"`
	GenSchema       cmd.GenSchemaCmd       `cmd:"gen-schema" help:"Generate the JSON schema for the cpak manifest"`
	Validate        cmd.ValidateCmd        `cmd:"validate" help:"Validate a cpak manifest"`
	Doctor          cmd.DoctorCmd          `cmd:"doctor" help:"Check host support for the cpak runtime"`
	MigrateManifest cmd.MigrateManifestCmd `cmd:"migrate-manifest" help:"Migrate a manifest to version 2"`
	HostExecServer  cmd.HostExecServerCmd  `cmd:"hostexec-server" help:"Start the hostexec server"`
	HostExecClient  cmd.HostExecClientCmd  `cmd:"hostexec-client" help:"Start the hostexec client"`

	cli.Base
}

var version = "0.0.1"

func main() {
	app, err := cli.New(&CLI{}, cli.WithVersion(version))
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
