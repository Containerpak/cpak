/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"path"
	"sort"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// describedHost is a machine written down rather than one the test is running
// on. Every mount decision below is checked against a host that does not exist,
// which is the point of stating one.
func describedHost(env map[string]string, files []string, sockets []string) Host {
	present := make(map[string]bool, len(files)+len(sockets))
	for _, file := range files {
		present[file] = true
	}
	isSocket := make(map[string]bool, len(sockets))
	for _, socket := range sockets {
		present[socket] = true
		isSocket[socket] = true
	}
	return Host{
		UID:    1000,
		Home:   "/home/ada",
		Getenv: func(name string) string { return env[name] },
		Glob: func(pattern string) ([]string, error) {
			matches := []string{}
			for candidate := range present {
				if ok, err := path.Match(pattern, candidate); err == nil && ok {
					matches = append(matches, candidate)
				}
			}
			sort.Strings(matches)
			return matches, nil
		},
		Exists:   func(candidate string) bool { return present[candidate] },
		IsSocket: func(candidate string) bool { return isSocket[candidate] },
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMountsAnswerForTheDescribedHost(t *testing.T) {
	host := describedHost(
		map[string]string{"WAYLAND_DISPLAY": "wayland-7"},
		[]string{"/run/user/1000/wayland-7.lock"},
		[]string{"/run/user/1000/wayland-7"},
	)

	mounts, _ := OverrideMounts(types.Override{SocketWayland: true}, host)

	if !contains(mounts, "/run/user/1000/wayland-7") {
		t.Fatalf("mounts %v do not contain the compositor socket of the described host", mounts)
	}
	if !contains(mounts, "/run/user/1000/wayland-7.lock") {
		t.Fatalf("mounts %v do not contain the display lock the described host has", mounts)
	}
}

func TestMountsRefuseADisplayOutsideTheRuntimeDirectory(t *testing.T) {
	host := describedHost(map[string]string{"WAYLAND_DISPLAY": "/tmp/foreign-wayland"}, nil, nil)

	mounts, _ := OverrideMounts(types.Override{SocketWayland: true}, host)

	if contains(mounts, "/tmp/foreign-wayland") {
		t.Fatalf("mounts %v accepted a socket outside /run/user/1000", mounts)
	}
}

func TestMountsOnAHostWithNothingOnIt(t *testing.T) {
	mounts, _ := OverrideMounts(types.Override{DeviceDri: true, DeviceSerial: true}, Host{})

	if len(mounts) != 1 || mounts[0] != "/dev/dri/" {
		t.Fatalf("mounts on an empty host: got %v, want only /dev/dri/", mounts)
	}
}

func TestAtSpiKeepsOnlySocketsUnderTheRuntimeDirectory(t *testing.T) {
	host := describedHost(
		map[string]string{"AT_SPI_BUS_ADDRESS": "unix:path=/tmp/foreign-at-spi,guid=test"},
		[]string{"/tmp/foreign-at-spi"},
		[]string{"/run/user/1000/at-spi/bus_9"},
	)

	paths := AtSpiSocketPaths(host)

	if contains(paths, "/tmp/foreign-at-spi") {
		t.Fatalf("AT-SPI paths %v accepted an address outside the runtime directory", paths)
	}
	if !contains(paths, "/run/user/1000/at-spi/bus_9") {
		t.Fatalf("AT-SPI paths %v did not find the bus the host has", paths)
	}
}

func TestHomeMountEndsInASeparator(t *testing.T) {
	mounts, _ := OverrideMounts(types.Override{FsHostHome: true}, describedHost(nil, nil, nil))

	if !contains(mounts, "/home/ada/") {
		t.Fatalf("mounts %v do not contain the home directory as a directory", mounts)
	}
}

func TestBluetoothAndTheSystemBusAreOneMount(t *testing.T) {
	mounts, _ := OverrideMounts(types.Override{SocketBluetooth: true, SocketSystemBus: true}, Host{})

	count := 0
	for _, mount := range mounts {
		if mount == "/run/dbus/system_bus_socket" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the system bus socket appears %d times in %v, want once", count, mounts)
	}
}
