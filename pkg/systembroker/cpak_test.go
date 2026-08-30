/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestCpakHostActionsAreLimitedByCapability(t *testing.T) {
	tests := []struct {
		name         string
		capabilities map[string]bool
		request      CpakRequest
		allowed      bool
	}{
		{
			name:         "read catalog",
			capabilities: map[string]bool{types.HostActionCpakRead: true},
			request:      CpakRequest{Arguments: []string{"discover", "list"}},
			allowed:      true,
		},
		{
			name:         "install requires manage",
			capabilities: map[string]bool{types.HostActionCpakRead: true},
			request:      CpakRequest{Arguments: []string{"discover", "install", "github.com/containerpak/ubuntu"}},
		},
		{
			name:         "create environment",
			capabilities: map[string]bool{types.HostActionCpakManage: true},
			request:      CpakRequest{Arguments: []string{"environment", "create", "--name", "Ubuntu", "--origin", "github.com/containerpak/ubuntu", "--json"}},
			allowed:      true,
		},
		{
			name:         "shell requires a terminal",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"environment", "shell", "--environment", "env-id", "--command", "/bin/bash"}},
		},
		{
			name:         "interactive shell",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"environment", "shell", "--environment", "env-id", "--command", "/bin/bash"}, Interactive: true},
			allowed:      true,
		},
		{
			name:         "interactive terminal shell with arguments",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"environment", "shell", "--environment", "env-id", "--terminal", "--command", "/bin/bash", "--", "-i"}, Interactive: true, Rows: 41, Columns: 132},
			allowed:      true,
		},
		{
			name:         "terminal size requires an interactive request",
			capabilities: map[string]bool{types.HostActionCpakRead: true},
			request:      CpakRequest{Arguments: []string{"discover", "list"}, Rows: 41, Columns: 132},
		},
		{
			name:         "terminal size requires both dimensions",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"environment", "shell", "--environment", "env-id", "--command", "/bin/bash"}, Interactive: true, Rows: 41},
		},
		{
			name:         "shell arguments require a separator",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"environment", "shell", "--environment", "env-id", "--terminal", "--command", "/bin/bash", "-i"}, Interactive: true},
		},
		{
			name:         "arbitrary cpak command",
			capabilities: map[string]bool{types.HostActionCpakExec: true},
			request:      CpakRequest{Arguments: []string{"run", "github.com/example/app"}, Interactive: true},
		},
		{
			name:         "flag injection",
			capabilities: map[string]bool{types.HostActionCpakManage: true},
			request:      CpakRequest{Arguments: []string{"discover", "install", "--version"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateCpakRequest(test.capabilities, test.request)
			if test.allowed && err != nil {
				t.Fatal(err)
			}
			if !test.allowed && err == nil {
				t.Fatal("cpak host action was accepted")
			}
		})
	}
}

func TestCpakSignalRequiresAPositivePID(t *testing.T) {
	request := CpakRequest{Arguments: []string{
		"environment", "signal", "--environment", "env-id", "--pid", "0", "--signal", "TERM",
	}}
	if _, err := validateCpakRequest(map[string]bool{types.HostActionCpakManage: true}, request); err == nil {
		t.Fatal("a non-positive process ID was accepted")
	}
}
