/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestEscalationPrefersTheToolTheSessionCanAnswer(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if got := escalationTools()[0]; got != "sudo" {
		t.Fatalf("a terminal session starts with %s", got)
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if got := escalationTools()[0]; got != "pkexec" {
		t.Fatalf("a graphical session starts with %s", got)
	}
}

func TestEscalationCoversEveryToolItKnows(t *testing.T) {
	tools := escalationTools()
	for _, want := range []string{"pkexec", "run0", "sudo", "doas"} {
		found := false
		for _, name := range tools {
			found = found || name == want
		}
		if !found {
			t.Fatalf("%s is not among the tools cpak would try: %v", want, tools)
		}
	}
}

func TestEscalationReportsWhenTheHostHasNoTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := escalationTool()
	if err == nil {
		t.Fatal("a host without any escalation tool was accepted")
	}
	for _, want := range []string{"sudo", "doas", "pkexec", "run0", "root"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not mention %s: %v", want, err)
		}
	}
}

func TestTheWholeCommandIsRefusedUnderSudo(t *testing.T) {
	t.Setenv("SUDO_UID", "1000")
	err := refuseSudoedStore()
	if os.Geteuid() != 0 {
		if err != nil {
			t.Fatalf("an unprivileged run was refused: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatal("a sudoed run was accepted, so the package would be looked up in the store of root")
	}
	t.Setenv("SUDO_UID", "")
	if err := refuseSudoedStore(); err != nil {
		t.Fatalf("a genuine root login was refused: %v", err)
	}
}
