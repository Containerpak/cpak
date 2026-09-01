/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	case "private":
		err = requireFile("/opt/cpak-integration/private")
	case "addon":
		err = probeAddon()
	case "loopback":
		err = probeLoopback()
	case "network":
		err = probeNetwork(argument(2), true)
	case "network-slow":
		err = probeNetwork(argument(2), true)
		if err == nil {
			time.Sleep(time.Second)
		}
	case "network-disabled":
		err = probeNetwork(argument(2), false)
	case "guest-environment":
		err = probeGuestEnvironment()
	case "seccomp":
		err = probeSeccomp()
	case "nested-mount":
		err = probeNestedMount(true)
	case "blocked-mount":
		err = probeNestedMount(false)
	case "nested-mount-child":
		err = probeNestedMountChild()
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
	case "root-identity":
		err = probeRootIdentity()
	case "user-manager-mock":
		err = runUserManagerMock(argument(2))
	default:
		err = fmt.Errorf("unknown probe %q", os.Args[1])
	}
	if err != nil {
		fail(err.Error())
	}
	fmt.Printf("%s probe passed\n", os.Args[1])
}

func probeSeccomp() error {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			values[fields[0]] = fields[1]
		}
	}
	if values["NoNewPrivs:"] != "1" || values["Seccomp:"] != "2" {
		return fmt.Errorf("sandbox status: NoNewPrivs=%s Seccomp=%s", values["NoNewPrivs:"], values["Seccomp:"])
	}
	return nil
}

type userManagerProperties struct {
	environment []string
}

func (p userManagerProperties) Get(interfaceName, property string) (dbus.Variant, *dbus.Error) {
	if interfaceName != "org.freedesktop.systemd1.Manager" || property != "Environment" {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("unknown user manager property"))
	}
	return dbus.MakeVariant(p.environment), nil
}

func runUserManagerMock(waylandDisplay string) error {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer connection.Close()
	if err = connection.Export(userManagerProperties{
		environment: []string{"WAYLAND_DISPLAY=" + waylandDisplay, "DISPLAY=:0"},
	}, "/org/freedesktop/systemd1", "org.freedesktop.DBus.Properties"); err != nil {
		return err
	}
	result, err := connection.RequestName("org.freedesktop.systemd1", dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if result != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("org.freedesktop.systemd1 is already owned")
	}
	select {}
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

func probeRootIdentity() error {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		return fmt.Errorf("identity is %d:%d", os.Geteuid(), os.Getegid())
	}
	temporary, err := os.CreateTemp("/tmp", "cpak-root-identity-")
	if err != nil {
		return fmt.Errorf("write temporary directory: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	return os.Remove(temporary.Name())
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

func probeGuestEnvironment() error {
	if got := os.Getenv("LANG"); got != "en_US.UTF-8" {
		return fmt.Errorf("LANG is %q", got)
	}
	if got := os.Getenv("LC_NUMERIC"); got != "ru_RU.UTF-8" {
		return fmt.Errorf("LC_NUMERIC is %q", got)
	}
	directories := strings.Split(os.Getenv("XDG_DATA_DIRS"), ":")
	want := []string{"/usr/local/share", "/usr/share", "/nix/store/desktop/share", "/run/current-system/sw/share"}
	if len(directories) != len(want) {
		return fmt.Errorf("XDG_DATA_DIRS is %q", os.Getenv("XDG_DATA_DIRS"))
	}
	for index := range want {
		if directories[index] != want[index] {
			return fmt.Errorf("XDG_DATA_DIRS is %q", os.Getenv("XDG_DATA_DIRS"))
		}
	}
	for _, path := range []string{
		"/usr/share/glib-2.0/schemas/gschemas.compiled",
		"/usr/lib/locale/en_US.utf8/LC_CTYPE",
		"/usr/lib/locale/ru_RU.utf8/LC_CTYPE",
	} {
		if err := requireFile(path); err != nil {
			return err
		}
	}
	return nil
}

func probeNestedMount(expected bool) error {
	command := exec.Command(os.Args[0], "nested-mount-child")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Credential:                 &syscall.Credential{Uid: 0, Gid: 0},
	}
	output, err := command.CombinedOutput()
	if expected {
		if err != nil {
			return fmt.Errorf("nested mount: %w: %s", err, output)
		}
		return nil
	}
	if err == nil {
		return fmt.Errorf("nested user namespace succeeded without permission")
	}
	if !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("nested user namespace returned %w: %s, want EPERM", err, output)
	}
	return nil
}

func probeNestedMountChild() error {
	temporary, err := os.MkdirTemp("/tmp", "cpak-nested-mount-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)

	tmpfsErr := syscall.Mount("tmpfs", temporary, "tmpfs", syscall.MS_NODEV|syscall.MS_NOSUID, "")
	if tmpfsErr == nil {
		defer syscall.Unmount(temporary, syscall.MNT_DETACH)
	}
	if err = syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_SLAVE, ""); err != nil {
		return fmt.Errorf("make / slave: %w; tmpfs mount: %v; %s", err, tmpfsErr, nestedMountContext())
	}
	if tmpfsErr != nil {
		return fmt.Errorf("mount tmpfs: %w; %s", tmpfsErr, nestedMountContext())
	}
	return nil
}

func nestedMountContext() string {
	status, _ := os.ReadFile("/proc/self/status")
	fields := make([]string, 0, 11)
	for _, line := range strings.Split(string(status), "\n") {
		for _, prefix := range []string{"Uid:", "Gid:", "CapEff:", "NoNewPrivs:", "Seccomp:", "Seccomp_filters:"} {
			if strings.HasPrefix(line, prefix) {
				fields = append(fields, strings.Join(strings.Fields(line), "="))
			}
		}
	}
	for _, path := range []string{
		"/proc/self/uid_map",
		"/proc/self/gid_map",
		"/proc/self/attr/current",
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns",
		"/proc/sys/kernel/unprivileged_userns_clone",
	} {
		mapping, err := os.ReadFile(path)
		if err == nil {
			fields = append(fields, filepath.Base(path)+"="+strings.Join(strings.Fields(string(mapping)), ":"))
		}
	}
	return strings.Join(fields, ", ")
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
