/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestACeilingOnlyAnswersForWhatItNames(t *testing.T) {
	requested := types.Override{
		SocketSessionBus: true,
		SocketSshAgent:   true,
		Env:              []string{"LANG=en_GB.UTF-8"},
	}
	ceiling := Ceiling{
		Present: true,
		Policy:  types.Override{},
		Named:   map[string]bool{"socketSessionBus": true},
	}

	effective := UnderCeiling(requested, ceiling, Host{})

	if effective.SocketSessionBus {
		t.Fatal("the ceiling did not close the permission it named")
	}
	if !effective.SocketSshAgent {
		t.Fatal("the ceiling closed a permission it never named")
	}
	if len(effective.Env) != 1 {
		t.Fatalf("environment: got %v, want the one the application asked for", effective.Env)
	}
}

func TestAnUnmanagedHostChangesNothing(t *testing.T) {
	requested := types.Override{SocketSessionBus: true, DeviceAll: true}

	effective := UnderCeiling(requested, Ceiling{}, Host{})

	if !reflect.DeepEqual(effective, requested) {
		t.Fatalf("a host with no ceiling narrowed a policy: got %+v", effective)
	}
}

func TestClosingEveryDeviceAlsoClosesTheBlanket(t *testing.T) {
	requested := types.Override{DeviceAll: true, DeviceDri: true}
	ceiling := Ceiling{
		Present: true,
		Policy:  types.Override{},
		Named:   map[string]bool{"deviceDri": true},
	}

	effective := UnderCeiling(requested, ceiling, Host{})

	if effective.DeviceAll {
		t.Fatal("closing a device left deviceAll open, which mounts all of them")
	}
}

func TestClosingTheBlanketLeavesASingleDeviceAlone(t *testing.T) {
	requested := types.Override{DeviceAll: true, DeviceDri: true}
	ceiling := Ceiling{
		Present: true,
		Policy:  types.Override{},
		Named:   map[string]bool{"deviceAll": true},
	}

	effective := UnderCeiling(requested, ceiling, Host{})

	if !effective.DeviceDri {
		t.Fatal("closing deviceAll also closed the GPU, which the ceiling never named")
	}
}

func TestFilesystemNarrowsToTheTighterAccess(t *testing.T) {
	granted := []types.FilesystemPermission{{Path: "home", Access: "read-only"}}
	requested := []types.FilesystemPermission{
		{Path: "home", Access: "read-write"},
		{Path: "/opt/data", Access: "read-write"},
	}

	result := IntersectFilesystem(granted, requested, Host{Home: "/home/ada"})

	if len(result) != 1 {
		t.Fatalf("filesystem: got %v, want only the granted scope", result)
	}
	if result[0].Path != "home" || result[0].Access != "read-only" {
		t.Fatalf("filesystem: got %v, want home read-only", result[0])
	}
}

func TestAHomeThatCannotBeResolvedIsNotAGrant(t *testing.T) {
	granted := []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	requested := []types.FilesystemPermission{{Path: "/home/ada/Documents", Access: "read-write"}}

	if result := IntersectFilesystem(granted, requested, Host{}); len(result) != 0 {
		t.Fatalf("filesystem: got %v on a host with no home, want nothing", result)
	}
	if result := IntersectFilesystem(granted, requested, Host{Home: "/home/ada"}); len(result) != 1 {
		t.Fatalf("filesystem: got %v on a host with a home, want the subpath", result)
	}
}

func TestALimitOfZeroLosesToANumber(t *testing.T) {
	if got := MinimumLimit(0, 512); got != 512 {
		t.Fatalf("limit: got %d, want 512", got)
	}
	if got := MinimumLimit(256, 512); got != 256 {
		t.Fatalf("limit: got %d, want 256", got)
	}
}

func TestAUserOverrideReplacesTheManifest(t *testing.T) {
	manifest := types.Override{SocketSessionBus: true, SocketWayland: true}
	user := types.Override{SocketWayland: true}

	effective, source := EffectiveOverride(manifest, &user, Ceiling{}, Host{})

	if source != PolicyFromUser {
		t.Fatalf("source: got %s, want %s", source, PolicyFromUser)
	}
	if effective.SocketSessionBus {
		t.Fatal("an owner who denied the session bus was given it by the manifest")
	}
}

func TestIntersectionIncludesVersionThreeCapabilities(t *testing.T) {
	parent := types.Override{
		DisplayX11:  true,
		Bluetooth:   true,
		Network:     true,
		HostNetwork: true,
		SessionBus: types.DBusPolicy{
			Own: []string{"org.example.Editor"},
		},
	}
	child := types.Override{
		DisplayX11:  true,
		Bluetooth:   false,
		Network:     true,
		HostNetwork: true,
		SessionBus: types.DBusPolicy{
			Own: []string{"org.example.Editor", "org.example.Other"},
		},
	}

	effective := Intersect(parent, child, Host{})
	if !effective.DisplayX11 || effective.Bluetooth || !effective.HostNetwork {
		t.Fatalf("v3 capabilities: got %+v", effective)
	}
	if len(effective.SessionBus.Own) != 1 || effective.SessionBus.Own[0] != "org.example.Editor" {
		t.Fatalf("session bus: got %+v", effective.SessionBus)
	}
}

func TestPermissionCatalogDoesNotTeachRemovedVersionThreeFields(t *testing.T) {
	for _, permission := range PermissionCatalog() {
		if permission.ManifestV3 && manifestV3RemovedPermission(permission.Key) {
			t.Fatalf("removed v3 permission is still stated: %s", permission.Key)
		}
	}
}
