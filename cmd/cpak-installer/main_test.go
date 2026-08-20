/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
)

func TestInstallCpakIsAtomicAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := []byte("cpak binary")
	path, changed, err := installCpak(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || path != filepath.Join(home, ".local", "bin", "cpak") {
		t.Fatalf("unexpected install result: %s %v", path, changed)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0755 {
		t.Fatalf("unexpected mode: %v", stat.Mode())
	}
	_, changed, err = installCpak(payload)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unchanged payload was reinstalled")
	}
}

func TestInstallCompanionUsesTheCpakBinDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := []byte("fvs service")
	changed, err := installStorageService(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("new companion was not installed")
	}
	got, err := os.ReadFile(filepath.Join(home, ".local", "bin", "cpak-storaged"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("companion payload: got %q, want %q", got, payload)
	}
}

func TestInstallMigratesStorageBeforeInstallingTheApplication(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	payload := []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/calls\"\n")
	capsule := bootstrap.Capsule{
		Metadata:  bootstrap.Metadata{Name: "Demo", Origin: "github.com/containerpak/demo"},
		Payload:   payload,
		Companion: []byte("storage service"),
	}
	if err := install(capsule, func(string) {}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != "storage migrate" || lines[1] != "install --yes github.com/containerpak/demo" {
		t.Fatalf("commands = %q", lines)
	}
}

func TestGUIProgressLabelHidesCommandOutput(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{message: "cpak is ready", want: "cpak is ready"},
		{message: "Installed cpak in ~/.local/bin", want: "cpak is ready"},
		{message: "Resolving Bottles", want: "Preparing Bottles"},
		{message: "Downloading sha256:abc", want: "Downloading Bottles"},
		{message: "Downloading runtime source google-chrome.deb (140188716 bytes)", want: "Downloading google-chrome.deb"},
		{message: "Verified runtime source google-chrome.deb", want: "Verified google-chrome.deb"},
		{message: "Using cached runtime source google-chrome.deb", want: "Preparing google-chrome.deb"},
		{message: "Installing runtime source google-chrome.deb", want: "Installing google-chrome.deb"},
		{message: "Extracting layer", want: "Installing Bottles"},
		{message: "Resolved commit f2700afd2980dda29a73284f8b182e32c2071d5cb4fc9b7ac72579641b3cbb", want: ""},
	}
	for _, test := range tests {
		if got := guiProgressLabel(test.message, "Bottles"); got != test.want {
			t.Fatalf("guiProgressLabel(%q) = %q, want %q", test.message, got, test.want)
		}
	}
}
