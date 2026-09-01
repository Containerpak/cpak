/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestNetworkHelperIsOptional(t *testing.T) {
	t.Setenv("PATH", "")
	plan, err := resolveUserNetwork(false, false)
	if err != nil {
		t.Fatalf("resolve disabled network: %v", err)
	}
	if plan != nil {
		t.Fatalf("got a helper for disabled network: %+v", plan)
	}
	if _, err = resolveUserNetwork(true, false); err == nil {
		t.Fatal("enabled network started without a userspace network helper")
	}
	if plan, err = resolveUserNetwork(true, true); err != nil || plan != nil {
		t.Fatalf("host network resolved a userspace helper: plan=%+v err=%v", plan, err)
	}
	if _, err = resolveUserNetwork(false, true); err == nil {
		t.Fatal("host network was accepted without network access")
	}
}

func TestHostNetworkPermissionSharesOnlyWhenExplicit(t *testing.T) {
	private := containerNamespaceOptions(types.Override{Network: true})
	if !private.IsolateNetwork {
		t.Fatal("ordinary network access shared the host network namespace")
	}
	host := containerNamespaceOptions(types.Override{Network: true, HostNetwork: true})
	if host.IsolateNetwork {
		t.Fatal("host network permission kept a private network namespace")
	}
}

func TestNetworkPermissionKeepsTheHostNamespacePrivate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		options := containerNamespaceOptions(types.Override{Network: enabled})
		if !options.IsolateNetwork {
			t.Fatalf("network=%v shares the host network namespace", enabled)
		}
	}
}

func TestSlirpNetworkDoesNotExposeTheHost(t *testing.T) {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	defer readyWriter.Close()
	exitReader, exitWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer exitReader.Close()
	defer exitWriter.Close()

	command := (&userNetworkPlan{path: "/usr/bin/slirp4netns"}).command(123, readyWriter, exitReader)
	for _, argument := range []string{
		"--configure",
		"--disable-host-loopback",
		"--enable-sandbox",
		"--enable-seccomp",
		"--ready-fd=3",
		"--exit-fd=4",
		"123",
		"tap0",
	} {
		if !slices.Contains(command.Args, argument) {
			t.Fatalf("network command does not contain %q: %q", argument, command.Args)
		}
	}
	if len(command.ExtraFiles) != 2 || command.ExtraFiles[0] != readyWriter || command.ExtraFiles[1] != exitReader {
		t.Fatalf("unexpected helper descriptors: %v", command.ExtraFiles)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatal("network helper was not detached from the launching session")
	}
}

func TestSlirpReadinessAcceptsSupportedBytes(t *testing.T) {
	for _, response := range [][]byte{{1}, {'1'}} {
		if err := readNetworkReady(bytes.NewReader(response)); err != nil {
			t.Fatalf("read slirp readiness %v: %v", response, err)
		}
	}
	for _, response := range [][]byte{{0}, {'0'}, {}} {
		if err := readNetworkReady(bytes.NewReader(response)); err == nil {
			t.Fatalf("accepted invalid slirp readiness response %v", response)
		}
	}
}

func TestContainerNetworkRequiresTheRecordedHelper(t *testing.T) {
	started, err := processStartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	container := types.Container{
		NetworkHelperPid:       os.Getpid(),
		NetworkHelperStartTime: started,
	}
	if !containerNetworkAlive(container, types.Override{Network: true}) {
		t.Fatal("the live recorded network helper was rejected")
	}
	container.NetworkHelperStartTime++
	if containerNetworkAlive(container, types.Override{Network: true}) {
		t.Fatal("a reused network helper PID was accepted")
	}
	container = types.Container{}
	if containerNetworkAlive(container, types.Override{Network: true}) {
		t.Fatal("an old container without network runtime state was accepted")
	}
	if !containerNetworkAlive(container, types.Override{}) {
		t.Fatal("a network-disabled container required a helper")
	}
	if !containerNetworkAlive(container, types.Override{Network: true, HostNetwork: true}) {
		t.Fatal("a host-network container required a userspace helper")
	}
}

func TestCleanupNetworkHelperTerminatesTheRecordedProcess(t *testing.T) {
	command := exec.Command("sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started, err := processStartTime(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	cleanupNetworkHelper(types.Container{
		NetworkHelperPid:       command.Process.Pid,
		NetworkHelperStartTime: started,
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		_ = command.Process.Kill()
		t.Fatal("network helper survived cleanup")
	}
	if err = syscall.Kill(command.Process.Pid, 0); err == nil {
		t.Fatal("network helper is still running")
	}
}
