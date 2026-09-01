/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

func TestNetworkSupervisorKeepsTheLifecycleIdentity(t *testing.T) {
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

	command := (&userNetworkPlan{path: "/usr/bin/slirp4netns"}).supervisorCommand("/usr/bin/cpak", 123, readyWriter, exitReader)
	for _, argument := range []string{
		"network-helper",
		"--slirp-path",
		"/usr/bin/slirp4netns",
		"--namespace-pid",
		"123",
		"--ready-fd=3",
		"--exit-fd=4",
	} {
		if !slices.Contains(command.Args, argument) {
			t.Fatalf("network supervisor command does not contain %q: %q", argument, command.Args)
		}
	}
	if len(command.ExtraFiles) != 2 || command.ExtraFiles[0] != readyWriter || command.ExtraFiles[1] != exitReader {
		t.Fatalf("unexpected supervisor descriptors: %v", command.ExtraFiles)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatal("network supervisor was not detached from the launching session")
	}
}

func TestNetworkSupervisorRefreshesChangedResolver(t *testing.T) {
	directory := t.TempDir()
	resolver := filepath.Join(directory, "resolv.conf")
	if err := os.WriteFile(resolver, []byte("nameserver 192.0.2.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "starts")
	helperPath := filepath.Join(directory, "slirp4netns")
	helper := "#!/bin/sh\n" +
		"printf '%s\\n' \"$$\" >> \"$CPAK_NETWORK_TEST_LOG\"\n" +
		"printf '\\001' >&3\n" +
		"trap 'exit 0' TERM INT\n" +
		"while read -r ignored <&4; do :; done\n"
	if err := os.WriteFile(helperPath, []byte(helper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_NETWORK_TEST_LOG", logPath)

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

	supervisor := userNetworkSupervisor{
		plan:         &userNetworkPlan{path: helperPath},
		namespacePID: os.Getpid(),
		resolverPath: resolver,
		period:       10 * time.Millisecond,
	}
	result := make(chan error, 1)
	go func() { result <- supervisor.run(readyWriter, exitReader) }()
	if err = readNetworkReady(readyReader); err != nil {
		t.Fatalf("network supervisor readiness: %v", err)
	}
	waitForNetworkStarts(t, logPath, 1)
	if err = os.WriteFile(resolver, []byte("nameserver 198.51.100.1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForNetworkStarts(t, logPath, 2)
	select {
	case err = <-result:
		t.Fatalf("network supervisor exited during resolver refresh: %v", err)
	default:
	}
	if err = exitWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if err != nil {
			t.Fatalf("network supervisor lifecycle: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("network supervisor survived its container")
	}
}

func waitForNetworkStarts(t *testing.T, path string, count int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil && bytes.Count(content, []byte{'\n'}) >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("userspace network helper did not start %d times", count)
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
