/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/systemauthority"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func useHostCeiling(t *testing.T, policy types.Override) {
	t.Helper()
	previous := hostCeiling
	hostCeiling = func() systemauthority.Ceiling { return systemauthority.Ceiling{Present: true, Policy: policy} }
	t.Cleanup(func() { hostCeiling = previous })
}

func useNoHostCeiling(t *testing.T) {
	t.Helper()
	previous := hostCeiling
	hostCeiling = func() systemauthority.Ceiling { return systemauthority.Ceiling{} }
	t.Cleanup(func() { hostCeiling = previous })
}

func wideApplication() types.Application {
	return types.Application{
		Origin:  "github.com/containerpak/demo",
		Version: "1",
		ParsedOverride: types.Override{
			Network:    true,
			SocketX11:  true,
			DeviceAll:  true,
			Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-write"}},
			HostActions: []types.HostActionGrant{
				{Provider: "containers", Capabilities: []string{"list", "start"}},
			},
		},
	}
}

// A host nobody configured must behave exactly as it did before the ceiling
// existed. Every installation in the world is in this state.
func TestAnUnmanagedHostLeavesThePolicyAlone(t *testing.T) {
	useNoHostCeiling(t)
	app := wideApplication()
	if got := resolvedOverride(app); !integrity.Restricts(app.ParsedOverride, got) || !integrity.Restricts(got, app.ParsedOverride) {
		t.Fatalf("a host with no ceiling changed the policy: %+v", got)
	}
}

// The ceiling is the answer to a question a signature cannot reach: not who
// published the application, but how much it may do here.
func TestTheCeilingHoldsAnApplicationTheManifestAsksMoreFor(t *testing.T) {
	useHostCeiling(t, types.Override{
		SocketX11:  true,
		Filesystem: []types.FilesystemPermission{{Path: "home", Access: "read-only"}},
	})
	got := resolvedOverride(wideApplication())
	if got.Network {
		t.Fatal("the network the manifest asked for survived a ceiling that does not allow it")
	}
	if got.DeviceAll {
		t.Fatal("every device the manifest asked for survived the ceiling")
	}
	if len(got.HostActions) != 0 {
		t.Fatalf("a host action survived a ceiling that grants none: %+v", got.HostActions)
	}
	if len(got.Filesystem) != 1 || got.Filesystem[0].Access != "read-only" {
		t.Fatalf("read-write access survived a read-only ceiling: %+v", got.Filesystem)
	}
	if !got.SocketX11 {
		t.Fatal("the ceiling took away something it allows")
	}
}

// The owner of a machine may narrow their own applications, and the ceiling
// must not give back what they took away.
func TestTheCeilingDoesNotWidenWhatTheOwnerNarrowed(t *testing.T) {
	useHostCeiling(t, types.Override{Network: true, SocketX11: true, DeviceDri: true})
	app := types.Application{
		Origin:         "github.com/containerpak/demo",
		Version:        "1",
		ParsedOverride: types.Override{SocketX11: true},
	}
	got := resolvedOverride(app)
	if got.Network || got.DeviceDri {
		t.Fatalf("the ceiling granted what the application never asked for: %+v", got)
	}
}

// The property an administrator is buying: whatever the manifest and the owner
// agree on, the result is inside the ceiling.
func TestTheResultIsAlwaysInsideTheCeiling(t *testing.T) {
	ceiling := types.Override{
		SocketWayland: true,
		Filesystem:    []types.FilesystemPermission{{Path: "xdg-download", Access: "read-only"}},
		PidsMax:       64,
	}
	useHostCeiling(t, ceiling)
	app := wideApplication()
	app.ParsedOverride.PidsMax = 0
	got := resolvedOverride(app)
	if !integrity.Restricts(ceiling, got) {
		t.Fatalf("the effective policy reaches past the ceiling: %+v", got)
	}
	// A limit of zero is no limit, so the ceiling's has to be the one that stands.
	if got.PidsMax != 64 {
		t.Fatalf("an application asking for no process limit kept it: %d", got.PidsMax)
	}
}
