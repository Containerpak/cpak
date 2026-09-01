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
	if len(os.Args) < 2 {
		fail("a probe name is required")
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
	case "network":
		err = probeNetwork(argument(2), true)
	case "network-disabled":
		err = probeNetwork(argument(2), false)
	case "browser-server":
		err = runBrowserServer()
	case "browser-open":
		err = openBrowser(argument(2))
	case "browser-read":
		err = readBrowserLog()
	case "persistence-write":
		err = probePersistence(true)
	case "persistence-read":
		err = probePersistence(false)
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

func argument(index int) string {
	if len(os.Args) <= index || os.Args[index] == "" {
		fail("probe argument is required")
	}
	return os.Args[index]
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
	return fetchReady("http://127.0.0.1:18080/ready")
}

func probeNetwork(endpoint string, expected bool) error {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint)
	if !expected {
		if err == nil {
			response.Body.Close()
			return fmt.Errorf("network-disabled package reached %s", endpoint)
		}
		return nil
	}
	if err != nil {
		return err
	}
	return checkReady(response)
}

func fetchReady(endpoint string) error {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	return checkReady(response)
}

func checkReady(response *http.Response) error {
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

const browserSocket = "/tmp/cpak-integration-browser.sock"
const browserLog = "/tmp/cpak-integration-browser.log"

func runBrowserServer() error {
	_ = os.Remove(browserSocket)
	listener, err := net.Listen("unix", browserSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	marker := fmt.Sprintf("server=%d\n", os.Getpid())
	if err = os.WriteFile(browserLog, []byte(marker), 0644); err != nil {
		return err
	}
	if _, err = os.Stdout.WriteString(marker); err != nil {
		return err
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return acceptErr
		}
		data, readErr := io.ReadAll(io.LimitReader(connection, 4097))
		closeErr := connection.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		uri := string(data)
		if uri == "" || len(uri) > 4096 || uri[len(uri)-1] != '\n' {
			return fmt.Errorf("invalid browser request")
		}
		file, openErr := os.OpenFile(browserLog, os.O_WRONLY|os.O_APPEND, 0644)
		if openErr != nil {
			return openErr
		}
		_, writeErr := file.WriteString(uri)
		closeErr = file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

func openBrowser(uri string) error {
	if len(uri) > 4096 || uri == "" || filepath.IsAbs(uri) {
		return fmt.Errorf("invalid browser URI")
	}
	connection, err := net.DialTimeout("unix", browserSocket, 3*time.Second)
	if err != nil {
		return err
	}
	if _, err = io.WriteString(connection, uri+"\n"); err != nil {
		connection.Close()
		return err
	}
	return connection.Close()
}

func readBrowserLog() error {
	content, err := os.ReadFile(browserLog)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(content)
	return err
}

func probePersistence(write bool) error {
	path := filepath.Join(os.Getenv("HOME"), ".cpak-integration-persistence")
	if write {
		return os.WriteFile(path, []byte("present\n"), 0644)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(content) != "present\n" {
		return fmt.Errorf("persistent home returned %q", content)
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
