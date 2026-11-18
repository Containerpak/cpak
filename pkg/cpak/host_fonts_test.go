/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/core"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestNativeHostFontsRequirePermission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := t.TempDir()
	cp := Cpak{}
	container := types.Container{StatePath: state}
	arguments, err := cp.nativeFontArguments(container, types.Override{})
	if err != nil || len(arguments) != 0 {
		t.Fatalf("fonts were exposed without permission: %v %v", arguments, err)
	}
	files, err := os.ReadDir(state)
	if err != nil || len(files) != 0 {
		t.Fatal("a disabled font grant wrote runtime files")
	}
	policy := types.Override{HostFonts: true}
	arguments, err = cp.nativeFontArguments(container, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(arguments, " "), "--writable-links "+filepath.Join(state, "fontconfig-cache")+":"+hostFontCacheTarget) {
		t.Fatal("fonts did not get a private cache")
	}
	app := types.Application{Config: `{}`}
	environment, err := containerEnvironment(app, policy, container)
	if err != nil || environmentValue(environment, "FONTCONFIG_FILE") != hostFontConfigTarget {
		t.Fatalf("native fontconfig did not receive its configuration: %v", err)
	}
	app.Flatpak = &types.FlatpakSource{}
	t.Setenv("FONTCONFIG_FILE", "")
	environment, err = containerEnvironment(app, policy, container)
	if err != nil || environmentValue(environment, "FONTCONFIG_FILE") == hostFontConfigTarget {
		t.Fatal("native font configuration replaced Flatpak's configuration")
	}
}

func TestNativeHostFontsRefuseLinksToCpakState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(home, ".config", "cpak")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(state, filepath.Join(home, ".fonts")); err != nil {
		t.Fatal(err)
	}
	cp := Cpak{}
	if _, err := cp.nativeFontArguments(types.Container{StatePath: t.TempDir()}, types.Override{HostFonts: true}); err == nil {
		t.Fatal("a font-directory symlink exposed cpak configuration")
	}
}

func TestNativeHostFontconfigReadsFontsWithoutWritingHostFiles(t *testing.T) {
	for _, name := range []string{"bwrap", "fc-list", "fc-cache"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	font := ""
	_ = filepath.WalkDir("/usr/share/fonts", func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".ttf") {
			font = path
			return filepath.SkipAll
		}
		return nil
	})
	if font == "" {
		t.Skip("no host TrueType font is available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(home, ".local", "share", "fonts")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(font)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "cpak-test.ttf"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	cp := Cpak{}
	arguments, err := cp.nativeFontArguments(types.Container{StatePath: state}, types.Override{HostFonts: true})
	if err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	command := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--bind", run, "/run", "--die-with-parent"}
	for i := 0; i < len(arguments); i += 2 {
		source, target, _ := strings.Cut(arguments[i+1], ":")
		bind := "--ro-bind"
		if arguments[i] == "--writable-links" {
			bind = "--bind"
		}
		command = append(command, bind, source, target)
	}
	command = append(command, "--setenv", "FONTCONFIG_FILE", hostFontConfigTarget, "/bin/sh", "-c",
		`set -e; fc-cache -f /run/host/user-fonts && fc-list --format '%{file}\n'; if printf changed >> /run/host/user-fonts/cpak-test.ttf 2>/dev/null; then exit 1; fi`)
	var output, stderr bytes.Buffer
	probe := exec.Command("bwrap", command...)
	probe.Stdout, probe.Stderr = &output, &stderr
	if err := probe.Run(); err != nil {
		t.Fatalf("fontconfig in a private namespace: %v\n%s\n%s", err, output.String(), stderr.String())
	}
	found := false
	for _, file := range strings.Split(output.String(), "\n") {
		found = found || file == "/run/host/user-fonts/cpak-test.ttf"
	}
	if !found {
		t.Fatalf("native fontconfig did not discover the host font: %s", output.String())
	}
	cached, err := os.ReadDir(filepath.Join(state, "fontconfig-cache"))
	if err != nil || len(cached) == 0 {
		t.Fatalf("fontconfig did not use its private cache: %v", err)
	}
}

func TestHostFontPermissionCannotGrowThroughNesting(t *testing.T) {
	for _, parent := range []bool{false, true} {
		for _, child := range []bool{false, true} {
			p, c := types.Override{HostFonts: parent}, types.Override{HostFonts: child}
			if got := core.Intersect(p, c, core.Host{}).HostFonts; got != (parent && child) {
				t.Fatalf("core host font permission: parent %v child %v result %v", parent, child, got)
			}
			if got := intersectOverrides(p, c).HostFonts; got != (parent && child) {
				t.Fatalf("nested host font permission: parent %v child %v result %v", parent, child, got)
			}
		}
	}
	if integrity.Restricts(types.Override{}, types.Override{HostFonts: true}) {
		t.Fatal("adding host fonts did not require permission consent")
	}
	if !integrity.Restricts(types.Override{HostFonts: true}, types.Override{}) {
		t.Fatal("removing host fonts was not treated as a restriction")
	}
}
