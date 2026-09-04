/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"strings"
	"testing"
)

func TestTheExecLineIsRewrittenHoweverItWasWritten(t *testing.T) {
	entry := "[Desktop Entry]\nType=Application\n Exec =/usr/bin/example %U\nTryExec=/usr/bin/example\n"

	exported := RewriteDesktopEntry(entry, DesktopExport{Launcher: "/usr/bin/cpak", Origin: "github.com/example/app"})

	if strings.Contains(exported, "Exec =/usr/bin/example") {
		t.Fatalf("a spaced Exec key was left for the launcher to run:\n%s", exported)
	}
	if !strings.Contains(exported, "Exec=/usr/bin/cpak run --desktop-launch github.com/example/app --desktop-file-span 0,0 @/usr/bin/example") {
		t.Fatalf("the exported entry does not run the application through cpak:\n%s", exported)
	}
}

func TestAQuotedCommandKeepsItsPath(t *testing.T) {
	rewritten := RewriteDesktopExec("/usr/bin/cpak", "github.com/example/app", `"/opt/Example App/example" --new-window %U`)

	want := `Exec=/usr/bin/cpak run --desktop-launch github.com/example/app --desktop-file-span 1,0 "@/opt/Example App/example" -- --new-window %U`
	if rewritten != want {
		t.Fatalf("rewritten Exec:\n got %s\nwant %s", rewritten, want)
	}
}

func TestRepairReplacesAStaleLauncherPath(t *testing.T) {
	entry := "[Desktop Entry]\nExec=\"/tmp/cpak test/cpak\" run --desktop-launch github.com/example/app @example -- %U\nTryExec=/tmp/cpak-test\n"
	repaired := RepairDesktopLauncher(entry, "/home/user/.local/bin/cpak")

	want := "Exec=/home/user/.local/bin/cpak run --desktop-launch github.com/example/app"
	if !strings.Contains(repaired, want) {
		t.Fatalf("stale launcher was not replaced:\n%s", repaired)
	}
	if !strings.Contains(repaired, "TryExec=/home/user/.local/bin/cpak") {
		t.Fatalf("stale TryExec was not replaced:\n%s", repaired)
	}
}

func TestASecondFilePlaceholderRemovesTheGrant(t *testing.T) {
	rewritten := RewriteDesktopExec("/usr/bin/cpak", "github.com/example/app", `/usr/bin/example %f --and %U`)

	if strings.Contains(rewritten, desktopFileSpanFlag) {
		t.Fatalf("an ambiguous entry received a file grant: %s", rewritten)
	}
}

func TestTheAliasSaysWhoseItIs(t *testing.T) {
	entry := "[Desktop Entry]\nType=Application\nName=Example\n"

	alias := DesktopAliasEntry(entry, DesktopExport{Origin: "github.com/example/app", CpakID: "example-1"})

	if DesktopEntryValue([]byte(alias), "X-cpak-Origin") != "github.com/example/app" {
		t.Fatalf("the alias does not name its origin:\n%s", alias)
	}
	if DesktopEntryValue([]byte(alias), "NoDisplay") != "true" {
		t.Fatalf("the alias would show a second time in the menu:\n%s", alias)
	}
}

func TestAnEntryWithNoDesktopGroupIsLeftAlone(t *testing.T) {
	entry := "Name=Example\n"

	if got := SetDesktopEntryValue(entry, "NoDisplay", "true"); got != entry {
		t.Fatalf("a file with no [Desktop Entry] group was given one: %q", got)
	}
}

func TestTheExportNameIsASinglePathElement(t *testing.T) {
	name := DesktopExportFileName("github.com/example/app@2.0", "/usr/share/applications/org.example.App.desktop")

	if strings.Contains(name, "/") {
		t.Fatalf("export name %q is not a single path element", name)
	}
	if !strings.HasSuffix(name, "org.example.App.desktop") {
		t.Fatalf("export name %q lost the entry it was made from", name)
	}
}
