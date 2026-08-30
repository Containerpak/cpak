/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

func main() {
	if len(os.Args) != 2 {
		fail("one probe name is required")
	}
	var err error
	switch os.Args[1] {
	case "bluez-mock":
		err = runBluezMock()
	case "bluetooth":
		err = probeBluetooth()
	case "desktop":
		err = probeDesktop()
	case "dependency":
		err = requireFile("/opt/cpak-integration/dependency")
	case "addon":
		err = probeAddon()
	case "loopback":
		err = probeLoopback()
	case "system-identities":
		err = probeSystemIdentities()
	default:
		err = fmt.Errorf("unknown probe %q", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
	fmt.Printf("%s probe passed\n", os.Args[1])
}

func probeSystemIdentities() error {
	if err := syscall.Setgroups([]int{65534}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setegid(65534); err != nil {
		return fmt.Errorf("setegid: %w", err)
	}
	if err := syscall.Seteuid(42); err != nil {
		return fmt.Errorf("seteuid: %w", err)
	}
	if os.Geteuid() != 42 || os.Getegid() != 65534 {
		return fmt.Errorf("identity is %d:%d", os.Geteuid(), os.Getegid())
	}
	if _, err := os.ReadDir("/"); err != nil {
		return fmt.Errorf("traverse environment root: %w", err)
	}
	temporary, err := os.CreateTemp("/tmp", "cpak-system-identity-")
	if err != nil {
		return fmt.Errorf("write environment temporary directory: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close environment temporary file: %w", err)
	}
	if err = os.Remove(temporary.Name()); err != nil {
		return fmt.Errorf("remove environment temporary file: %w", err)
	}
	return nil
}

func runBluezMock() error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer connection.Close()
	result, err := connection.RequestName("org.bluez", dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if result != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("org.bluez is already owned")
	}
	select {}
}

func probeBluetooth() error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer connection.Close()
	var owned bool
	call := connection.Object("org.freedesktop.DBus", dbus.ObjectPath("/org/freedesktop/DBus")).Call(
		"org.freedesktop.DBus.NameHasOwner",
		0,
		"org.bluez",
	)
	if err = call.Store(&owned); err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("org.bluez is not visible through the proxy")
	}
	return nil
}

func probeDesktop() error {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	display := os.Getenv("WAYLAND_DISPLAY")
	if runtime == "" || display == "" {
		return fmt.Errorf("Wayland environment is missing")
	}
	socket := filepath.Join(runtime, display)
	info, err := os.Stat(socket)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("Wayland endpoint is not a socket")
	}
	connection, err := net.DialTimeout("unix", socket, 3*time.Second)
	if err != nil {
		return err
	}
	return connection.Close()
}

func probeLoopback() error {
	response, err := http.Get("http://127.0.0.1:18080/ready")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("host server returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return err
	}
	if string(body) != "ready\n" {
		return fmt.Errorf("host server returned %q", body)
	}
	return nil
}

func probeAddon() error {
	if os.Getenv("CPAK_INTEGRATION_ADDON") != "present" {
		return fmt.Errorf("addon environment is missing")
	}
	return requireFile("/opt/cpak-integration/addon")
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
