/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
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

func TestBluetoothRequiresSharedNetwork(t *testing.T) {
	manifest := validManifestForTest()
	manifest.Override.SocketBluetooth = true
	manifest.Override.Network = false
	if err := (&Cpak{}).ValidateManifest(manifest); err == nil {
		t.Fatal("Bluetooth without network sharing was accepted")
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
