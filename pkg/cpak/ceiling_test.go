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

// useNamedHostCeiling is the ceiling as it comes off disk, which knows which
// permissions its file actually mentioned. useHostCeiling leaves that nil, and
// nil means all of them, so the two helpers cover both readings on purpose.
func useNamedHostCeiling(t *testing.T, policy types.Override, names ...string) {
	t.Helper()
	named := make(map[string]bool, len(names))
	for _, name := range names {
		named[name] = true
	}
	previous := hostCeiling
	hostCeiling = func() systemauthority.Ceiling {
		return systemauthority.Ceiling{Present: true, Policy: policy, Named: named}
	}
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

// An administrator who closes one door must not find they have closed every
// other one. The ceiling meets a policy by intersection, so before it recorded
// which permissions its file named, a file saying only that the session bus is
// closed also emptied the filesystem, the environment and every host action for
// every application on the host, because those are what an unwritten field
// intersects down to.
func TestACeilingOnlyHoldsBackThePermissionsItNames(t *testing.T) {
	closed := types.Override{}
	useNamedHostCeiling(t, closed, "socketSessionBus")

	application := wideApplication()
	application.ParsedOverride.SocketSessionBus = true
	resolved := resolvedOverride(application)

	if resolved.SocketSessionBus {
		t.Fatal("the ceiling named the session bus and did not close it")
	}
	if !resolved.Network || !resolved.SocketX11 || !resolved.DeviceAll {
		t.Fatalf("the ceiling closed permissions its file never named: %+v", resolved)
	}
	if len(resolved.Filesystem) == 0 {
		t.Fatal("the ceiling emptied the filesystem permissions it never named")
	}
	if len(resolved.HostActions) == 0 {
		t.Fatal("the ceiling dropped the host actions it never named")
	}
}

// The other half: a ceiling that names a permission still beats the manifest,
// and naming one permission is not a way to escape the rest of the file.
func TestACeilingStillBeatsTheManifestOnWhatItNames(t *testing.T) {
	useNamedHostCeiling(t, types.Override{Network: false, DeviceAll: false}, "network", "deviceAll")
	resolved := resolvedOverride(wideApplication())
	if resolved.Network {
		t.Fatal("an application kept the network a ceiling took away")
	}
	if resolved.DeviceAll {
		t.Fatal("an application kept every device a ceiling took away")
	}
	if !resolved.SocketX11 {
		t.Fatal("the ceiling reached a permission it did not name")
	}
}

// Naming every device permission one at a time and leaving deviceAll unnamed
// closed nothing: deviceAll mounts the whole of /dev, so it undoes the lot.
func TestClosingTheDevicesAlsoClosesTheKeyThatGrantsThemAll(t *testing.T) {
	useNamedHostCeiling(t, types.Override{},
		"deviceDri", "deviceKvm", "deviceShm", "deviceAlsa", "deviceVideo",
		"deviceFuse", "deviceTun", "deviceUsb", "deviceSerial", "deviceInput", "deviceTTY")
	resolved := resolvedOverride(wideApplication())
	if resolved.DeviceAll {
		t.Fatal("a ceiling over every device permission left the one that grants all of them")
	}
	mounts, _ := GetOverrideMounts(resolved)
	for _, mount := range mounts {
		if mount == "/dev/" {
			t.Fatalf("the whole of /dev is still mounted: %v", mounts)
		}
	}
}

// socketBluetooth mounts /run/dbus/system_bus_socket, which is the socket
// socketSystemBus mounts, so a ceiling over one that leaves the other open is
// not a ceiling at all.
func TestClosingEitherBusSocketClosesTheOtherName(t *testing.T) {
	for _, named := range []string{"socketSystemBus", "socketBluetooth"} {
		useNamedHostCeiling(t, types.Override{}, named)
		application := wideApplication()
		application.ParsedOverride.SocketSystemBus = true
		application.ParsedOverride.SocketBluetooth = true
		resolved := resolvedOverride(application)
		if resolved.SocketSystemBus || resolved.SocketBluetooth {
			t.Fatalf("a ceiling naming %s left the same socket reachable by its other name: %+v", named, resolved)
		}
	}
}

// The legacy v1 fields mount the places the typed list names, so a ceiling over
// the list has to reach them. It must not run the other way: naming one legacy
// field is narrow and must not empty every typed permission.
func TestAceilingOverTheFilesystemReachesTheLegacySpellings(t *testing.T) {
	useNamedHostCeiling(t, types.Override{}, "filesystem")
	application := wideApplication()
	application.ParsedOverride.FsHostHome = true
	application.ParsedOverride.FsHostEtc = true
	application.ParsedOverride.FsExtra = []string{"/var/lib"}
	resolved := resolvedOverride(application)
	if resolved.FsHostHome || resolved.FsHostEtc || len(resolved.FsExtra) != 0 {
		t.Fatalf("the legacy fields survived a ceiling over the filesystem: %+v", resolved)
	}

	useNamedHostCeiling(t, types.Override{}, "fsHostEtc")
	narrow := resolvedOverride(wideApplication())
	if len(narrow.Filesystem) == 0 {
		t.Fatal("naming one legacy field emptied every typed filesystem permission")
	}
}
