/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
)

func TestDesktopCallbackResolvesTheRegisteredApplicationInstance(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_SERVICE_SOCKET", filepath.Join(runtimeDirectory, "service.sock"))
	directory, err := desktopCallbackDirectory()
	if err != nil {
		t.Fatal(err)
	}
	state := "s" + strings.Repeat("t", 42)
	redirect := "ms-appx-web://microsoft.aad.brokerplugin/client"
	authorization := "https://login.example/authorize?redirect_uri=" + url.QueryEscape(redirect) + "&state=" + state
	callback := redirect + "?code=authorization-code&state=" + state
	if err = systembroker.RegisterDesktopCallback(directory, authorization, "github.com/example/app", "office-test"); err != nil {
		t.Fatal(err)
	}

	cp := Cpak{}
	instance, found, err := cp.ResolveDesktopCallbackInstance("github.com/example/app", []string{"--fixed-argument", callback})
	if err != nil {
		t.Fatal(err)
	}
	if !found || instance != "office-test" {
		t.Fatalf("resolved desktop callback: found %t, instance %q", found, instance)
	}
}

func TestDesktopCallbackIgnoresRegularDesktopArguments(t *testing.T) {
	t.Setenv("CPAK_SERVICE_SOCKET", "relative/service.sock")
	cp := Cpak{}
	instance, found, err := cp.ResolveDesktopCallbackInstance("github.com/example/app", []string{"--open", "/home/user/report.txt"})
	if err != nil || found || instance != "" {
		t.Fatalf("regular desktop arguments: found %t, instance %q, error %v", found, instance, err)
	}
}
