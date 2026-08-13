/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestBluetoothUsesOneSystemBusMount(t *testing.T) {
	mounts, _ := GetOverrideMounts(types.Override{SocketBluetooth: true, SocketSystemBus: true})
	count := 0
	for _, mount := range mounts {
		if mount == "/run/dbus/system_bus_socket" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("system bus mount count: %d", count)
	}
}

func TestKvmUsesDevicePath(t *testing.T) {
	mounts, _ := GetOverrideMounts(types.Override{DeviceKvm: true})
	if !slicesContain(mounts, "/dev/kvm") {
		t.Fatalf("KVM mounts %v do not contain the device path", mounts)
	}
	if slicesContain(mounts, "/dev/kvm/") {
		t.Fatalf("KVM mounts %v treat the device as a directory", mounts)
	}
}

func TestInputDevicesRequireTheirOwnPermission(t *testing.T) {
	usbMounts, _ := GetOverrideMounts(types.Override{DeviceUsb: true})
	if slicesContain(usbMounts, "/dev/input/") {
		t.Fatal("USB permission also exposed input devices")
	}
	inputMounts, _ := GetOverrideMounts(types.Override{DeviceInput: true})
	if !slicesContain(inputMounts, "/dev/input/") {
		t.Fatal("input device permission did not expose input devices")
	}
}

func TestAtSpiUsesSessionAddress(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	want := "/run/user/" + uid + "/at-spi/bus_9"
	t.Setenv("AT_SPI_BUS_ADDRESS", "unix:path="+want+",guid=test")

	mounts, _ := GetOverrideMounts(types.Override{SocketAtSpiBus: true})
	if !slicesContain(mounts, want) {
		t.Fatalf("AT-SPI mounts %v do not contain %s", mounts, want)
	}
}

func TestAtSpiRejectsAddressOutsideRuntimeDirectory(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	t.Setenv("AT_SPI_BUS_ADDRESS", "unix:path=/tmp/foreign-at-spi")

	for _, path := range atSpiSocketPaths(uid) {
		if path == "/tmp/foreign-at-spi" {
			t.Fatalf("accepted AT-SPI path outside the user runtime directory")
		}
	}
}

func TestWaylandUsesActiveDisplay(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	t.Setenv("WAYLAND_DISPLAY", "wayland-3")

	mounts, _ := GetOverrideMounts(types.Override{SocketWayland: true})
	want := "/run/user/" + uid + "/wayland-3"
	if !slicesContain(mounts, want) {
		t.Fatalf("Wayland mounts %v do not contain %s", mounts, want)
	}
}

func TestWaylandMountsDisplayLock(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "wayland-3")
	if err := os.WriteFile(socket+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}

	mounts := waylandSocketMounts(socket)
	if !slicesContain(mounts, socket+".lock") {
		t.Fatalf("Wayland mounts %v do not contain the display lock", mounts)
	}
}

func TestWaylandDisplayPreservesActiveName(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	t.Setenv("WAYLAND_DISPLAY", "wayland-4")

	if got := waylandDisplay(uid); got != "wayland-4" {
		t.Fatalf("Wayland display: got %s, want wayland-4", got)
	}
}

func TestWaylandDisplayAcceptsNestedRuntimeSocket(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	want := filepath.Join("/run/user", uid, "cpak-broker-test", "desktop", "wayland-0")
	t.Setenv("WAYLAND_DISPLAY", want)

	if got := waylandDisplay(uid); got != want {
		t.Fatalf("Wayland display: got %s, want %s", got, want)
	}
}

func TestWaylandRejectsDisplayOutsideRuntimeDirectory(t *testing.T) {
	uid := fmt.Sprintf("%d", os.Getuid())
	t.Setenv("WAYLAND_DISPLAY", "/tmp/foreign-wayland")

	mounts, _ := GetOverrideMounts(types.Override{SocketWayland: true})
	if slicesContain(mounts, "/tmp/foreign-wayland") {
		t.Fatalf("Wayland mounts accepted a socket outside /run/user/%s", uid)
	}
}

func TestLoadOverrideDoesNotCreateMissingState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := LoadOverride("github.com/example/app", "main")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing override error: %v", err)
	}
	path := filepath.Join(home, ".config", "cpak", "overrides")
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("loading created override state: %v", statErr)
	}
}

func TestSaveOverrideCreatesParentAndRoundTrips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := types.NewOverride()
	want.Filesystem = []types.FilesystemPermission{{Path: "home", Access: "read-write"}}
	if err := SaveOverride(want, "github.com/example/app", "main"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadOverride("github.com/example/app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override round trip: got %+v, want %+v", got, want)
	}
}

func TestBluetoothRequiresSharedNetwork(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.SocketBluetooth = true
	manifest.Override.Network = false
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("Bluetooth without network sharing was accepted")
	}
}

func TestSystemBrokerShimsAreExplicit(t *testing.T) {
	override := types.Override{
		Notification: true,
		OpenURI:      true,
		HostActions: []types.HostActionGrant{{
			Provider:     types.HostActionProviderContainers,
			Capabilities: []string{types.HostActionContainersRead},
		}},
	}
	shims := systemBrokerShims(override)
	if !reflect.DeepEqual(shims, []string{"notify-send", "xdg-open", "gio", "podman", "docker"}) {
		t.Fatalf("system broker shims: %v", shims)
	}
}

func validManifestForTest() *types.CpakManifest {
	return &types.CpakManifest{
		ManifestVersion: "2.0",
		Name:            "test",
		Description:     "test application",
		Image:           "ghcr.io/example/test:latest",
		Binaries:        []string{"/usr/bin/test"},
	}
}
