/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type completionTestRoot struct {
	List completionTestListCmd `cmd:"list" help:"List packages"`
	cli.Base
}

type completionTestListCmd struct {
	cli.Base
}

func TestCompletion(t *testing.T) {
	app, err := cli.New(&completionTestRoot{})
	if err != nil {
		t.Fatal(err)
	}
	app.SetName("cpak")

	for shell, expected := range map[string]string{
		"bash": "complete -F _cpak_completions cpak",
		"zsh":  "#compdef cpak",
		"fish": "complete -c cpak",
	} {
		t.Run(shell, func(t *testing.T) {
			var output bytes.Buffer
			command := CompletionCmd{Shell: shell}
			command.Configure(app, &output)
			if err := command.Run(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), expected) {
				t.Fatalf("%s completion does not target cpak: %q", shell, output.String())
			}
			if !strings.Contains(output.String(), "list") {
				t.Fatalf("%s completion does not include commands: %q", shell, output.String())
			}
		})
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	app, err := cli.New(&completionTestRoot{})
	if err != nil {
		t.Fatal(err)
	}
	command := CompletionCmd{Shell: "tcsh"}
	command.Configure(app, &bytes.Buffer{})
	if err := command.Run(); err == nil {
		t.Fatal("unsupported shell was accepted")
	}
}
