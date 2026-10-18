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
	"github.com/mirkobrombin/cpak/pkg/types"
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

func TestDesktopCallbackKeepsTheOriginalWaylandEndpoint(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_SERVICE_SOCKET", filepath.Join(directory, "service.sock"))
	original := "original-wayland"
	desktop := "desktop-wayland"
	override := types.Override{SocketWayland: true}
	t.Setenv("WAYLAND_DISPLAY", original)
	expected := currentWaylandEndpoint(override)
	t.Setenv("WAYLAND_DISPLAY", desktop)
	callbacks, err := desktopCallbackDirectory()
	if err != nil {
		t.Fatal(err)
	}
	state := "s" + strings.Repeat("t", 42)
	redirect := "sample-app://callback/return"
	origin := "github.com/example/app"
	authorization := "https://login.example/authorize?redirect_uri=" + url.QueryEscape(redirect) + "&state=" + state
	if err = systembroker.RegisterDesktopCallback(callbacks, authorization, origin, "test"); err != nil {
		t.Fatal(err)
	}
	cp := Cpak{}
	cp.SetDesktopLaunch(true)
	instance, found, err := cp.ResolveDesktopCallbackInstance(origin, []string{redirect + "?code=sample-code&state=" + state})
	if err != nil || !found || instance != "test" {
		t.Fatalf("resolve callback: instance %q, found %t, error %v", instance, found, err)
	}
	container := types.Container{Instance: "test", WaylandDisplay: expected.Display, WaylandSocketPath: expected.SocketPath}
	actual := cp.desktopCallbackWaylandEndpoint(origin, instance, container, override)
	if actual != expected {
		t.Fatalf("callback endpoint = %+v, want original %+v", actual, expected)
	}
	originalHash, err := containerLaunchPolicyHashWithWayland(1, "root", override, nil, nil, expected)
	if err != nil {
		t.Fatal(err)
	}
	callbackHash, err := containerLaunchPolicyHashWithWayland(1, "root", override, nil, nil, actual)
	if err != nil || callbackHash != originalHash {
		t.Fatalf("callback changed original policy: %q, error %v", callbackHash, err)
	}
	changed := override
	changed.SocketSessionBus = true
	changedHash, err := containerLaunchPolicyHashWithWayland(1, "root", changed, nil, nil, actual)
	if err != nil || changedHash == originalHash {
		t.Fatalf("callback ignored a permission change: %q, error %v", changedHash, err)
	}
	for _, entry := range []struct {
		name, origin, instance string
		launch                 bool
	}{
		{"regular launch", origin, "test", false},
		{"other package", "github.com/example/other", "test", true},
		{"other instance", origin, "other", true},
	} {
		t.Run(entry.name, func(t *testing.T) {
			cp.desktopLaunch = entry.launch
			if got := cp.desktopCallbackWaylandEndpoint(entry.origin, entry.instance, container, override); got != currentWaylandEndpoint(override) {
				t.Fatalf("unrelated launch adopted original endpoint: %+v", got)
			}
		})
	}
}

func TestDesktopCallbackContextIsClearedForTheNextLaunch(t *testing.T) {
	cp := Cpak{desktopCallbackOrigin: "github.com/example/app", desktopCallbackInstance: "test"}
	cp.SetDesktopLaunch(true)
	_, found, err := cp.ResolveDesktopCallbackInstance("github.com/example/app", []string{"/home/user/report.txt"})
	if err != nil || found || cp.desktopCallbackOrigin != "" || cp.desktopCallbackInstance != "" {
		t.Fatalf("callback context survived a regular launch: %+v, found %t, error %v", cp, found, err)
	}
}
