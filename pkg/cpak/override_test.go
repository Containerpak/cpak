/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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

func TestSystemBrokerOperationsAreExplicit(t *testing.T) {
	operations := systemBrokerOperations(types.Override{Notification: true, OpenURI: true})
	if !reflect.DeepEqual(operations, []string{"notify-send", "xdg-open"}) {
		t.Fatalf("system broker operations: %v", operations)
	}
	if commands := effectiveHostCommands(types.Override{Notification: true}); len(commands) != 0 {
		t.Fatalf("notifications still expose host commands: %v", commands)
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
