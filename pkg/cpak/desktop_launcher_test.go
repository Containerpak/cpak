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
		if !strings.Contains(string(content), desktopFileSpanFlag+" ") {
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

// The attack the span exists to stop. A publisher writes the old markers into
// its own Exec line, and no amount of filtering that string can be trusted,
// because the launcher unquotes it differently than a Go filter does. So the
// answer is not a better filter: nothing the publisher writes decides anything
// any more, and the counts come from cpak.
func TestAPublisherCannotSayWhichArgumentsAreFiles(t *testing.T) {
	// Every spelling that used to survive the filter and arrive as a marker.
	for _, hostile := range []string{
		legacyGrantMarkerStart,
		`""` + legacyGrantMarkerStart,
		`"` + legacyGrantMarkerStart + `"`,
		legacyGrantMarkerEnd,
	} {
		tokens := splitDesktopArguments("--open " + hostile + " /home/user/.ssh/id_ed25519 %U")
		span, selects, err := countDesktopFileSpan(tokens)
		if err != nil || !selects {
			t.Fatalf("the placeholder was not found in %q: %v", hostile, err)
		}
		// The placeholder is the last token, so only the last argument of a
		// launch is a file. The path the publisher planted is not.
		if span.After != 0 {
			t.Fatalf("the span put publisher text after the files: %+v", span)
		}
		total := span.Before + 2
		if span.selects(0, total) || span.selects(1, total) {
			t.Fatalf("%q made a publisher argument count as a selected file", hostile)
		}
		if !span.selects(span.Before, total) {
			t.Fatal("the file the user chose was not selected")
		}
	}
}

// An entry naming two placeholders cannot be described by two counts, and is
// exported without a file grant rather than with one cpak is guessing at.
func TestTwoPlaceholdersAreRefusedRatherThanGuessed(t *testing.T) {
	if _, _, err := countDesktopFileSpan(splitDesktopArguments("%f --and %U")); err == nil {
		t.Fatal("an entry naming two placeholders was described anyway")
	}
}

// A launch that carried no files selects nothing, which is what happens when an
// entry is started from the menu rather than by dropping a file on it.
func TestALaunchWithNoFilesSelectsNothing(t *testing.T) {
	span := desktopFileSpan{Before: 1, After: 1}
	for index := 0; index < 2; index++ {
		if span.selects(index, 2) {
			t.Fatalf("argument %d was taken for a file when none were passed", index)
		}
	}
}
