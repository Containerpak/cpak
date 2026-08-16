/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepairDesktopLaunchersUsesAbsoluteCpakPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	cpakEntry := filepath.Join(directory, "cpak-package-demo.desktop")
	aliasEntry := filepath.Join(directory, "demo.desktop")
	userEntry := filepath.Join(directory, "user.desktop")
	for path, content := range map[string]string{
		cpakEntry:  "[Desktop Entry]\nExec=cpak run github.com/containerpak/demo @demo -- %U\nTryExec=cpak\n",
		aliasEntry: "[Desktop Entry]\nExec=cpak run github.com/containerpak/demo @demo -- %U\nTryExec=cpak\nX-cpak-Origin=github.com/containerpak/demo\n",
		userEntry:  "[Desktop Entry]\nExec=cpak run user-command\nTryExec=cpak\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	launcher := "/home/demo user/.local/bin/cpak"
	if err := repairDesktopLaunchers(launcher); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{cpakEntry, aliasEntry} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), `Exec="/home/demo user/.local/bin/cpak" run`) {
			t.Fatalf("desktop entry does not use the absolute launcher: %s", content)
		}
		if !strings.Contains(string(content), "run --desktop-launch github.com/containerpak/demo") {
			t.Fatalf("desktop entry does not mark a desktop launch: %s", content)
		}
		if !strings.Contains(string(content), desktopFileArgumentStart+" %U "+desktopFileArgumentEnd) {
			t.Fatalf("desktop entry does not identify file arguments: %s", content)
		}
		if repaired := repairDesktopLauncher(string(content), launcher); repaired != string(content) {
			t.Fatalf("desktop launcher repair is not idempotent: %s", repaired)
		}
		if !strings.Contains(string(content), "TryExec="+launcher) {
			t.Fatalf("desktop entry does not use the absolute TryExec path: %s", content)
		}
	}

	content, err := os.ReadFile(userEntry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), launcher) {
		t.Fatalf("unowned desktop entry was modified: %s", content)
	}
}

func TestMarkDesktopFileArgumentsDropsInjectedMarkers(t *testing.T) {
	arguments := desktopFileArgumentStart + " /home/user/private " + desktopFileArgumentEnd + " %U"
	marked := markDesktopFileArguments(arguments)
	want := "/home/user/private " + desktopFileArgumentStart + " %U " + desktopFileArgumentEnd
	if marked != want {
		t.Fatalf("marked arguments: got %q, want %q", marked, want)
	}
}
