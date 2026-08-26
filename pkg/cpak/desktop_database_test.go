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

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestDesktopExportsRefreshHandlerDatabase(t *testing.T) {
	c := newTestCpak(t)
	directory := t.TempDir()
	logPath := filepath.Join(directory, "calls")
	executable := filepath.Join(directory, "update-desktop-database")
	content := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$CPAK_REFRESH_LOG\"\n"
	if err := os.WriteFile(executable, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("CPAK_REFRESH_LOG", logPath)

	layer := "desktop-layer"
	entry := "/usr/share/applications/example.desktop"
	seedDesktopLayer(t, c, layer, entry, "[Desktop Entry]\nName=Example\nExec=/usr/bin/example\nMimeType=text/plain;\n")
	app := types.Application{
		CpakId:               "desktop-id",
		Origin:               "github.com/containerpak/example",
		ParsedDesktopEntries: []string{entry},
		ParsedLayers:         []string{layer},
	}
	if err := c.createExports(app); err != nil {
		t.Fatal(err)
	}
	if err := c.removeExports(app); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
	if got := strings.Fields(string(data)); len(got) != 2 || got[0] != want || got[1] != want {
		t.Fatalf("expected two refreshes of %q, got %q", want, data)
	}
}
