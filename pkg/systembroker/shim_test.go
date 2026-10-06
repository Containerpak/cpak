/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"path/filepath"
	"testing"

	"github.com/creack/pty"
)

func TestOpenURIShimCarriesItsWorkingDirectory(t *testing.T) {
	directory := t.TempDir()
	request, err := parseOpenURI([]string{"."}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if request.URI != "." || request.WorkingDirectory != filepath.Clean(directory) {
		t.Fatalf("open URI request: %+v", request)
	}
}

func TestGIOOpenCarriesItsWorkingDirectory(t *testing.T) {
	directory := t.TempDir()
	request, err := parseGIOOpen([]string{"open", "download.txt"}, directory)
	if err != nil {
		t.Fatal(err)
	}
	if request.URI != "download.txt" || request.WorkingDirectory != filepath.Clean(directory) {
		t.Fatalf("GIO request: %+v", request)
	}
}

func TestOpenURIShimAcceptsMicrosoftIdentityCallbacks(t *testing.T) {
	base := "ms-appx-web://microsoft.aad.brokerplugin/d3590ed6-52b3-4102-aeff-aad2292ab01c"
	for _, query := range []string{"?state=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&code=test-code", "?state=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&error=access_denied"} {
		uri := base + query
		request, err := parseOpenURI([]string{uri}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := resolveOpenURI(request, nil)
		if err != nil || resolved != uri {
			t.Fatalf("identity callback was not preserved: %v", err)
		}
	}
}

func TestOpenURIRejectsInvalidMicrosoftIdentityCallbacks(t *testing.T) {
	for _, uri := range []string{
		"ms-appx-web://example.com/d3590ed6-52b3-4102-aeff-aad2292ab01c",
		"ms-appx-web://user@microsoft.aad.brokerplugin/d3590ed6-52b3-4102-aeff-aad2292ab01c",
		"ms-appx-web://microsoft.aad.brokerplugin:443/d3590ed6-52b3-4102-aeff-aad2292ab01c",
		"ms-appx-web://microsoft.aad.brokerplugin/other/path",
		"ms-appx-web://microsoft.aad.brokerplugin/d3590ed6-52b3-4102-aeff-aad2292ab01c#fragment",
		"ms-appx-web://microsoft.aad.brokerplugin/d3590ed6-52b3-4102-aeff-aad2292ab01c?state=%ZZ",
	} {
		if err := validateOpenURI(OpenURIRequest{URI: uri}); err == nil {
			t.Fatalf("invalid identity callback was accepted: %q", uri)
		}
	}
}

func TestCpakShimReadsTerminalSize(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 41, Cols: 132}); err != nil {
		t.Fatal(err)
	}

	rows, columns := shimTerminalSize(slave, true)
	if rows != 41 || columns != 132 {
		t.Fatalf("cpak shim terminal size: %dx%d", columns, rows)
	}
}
