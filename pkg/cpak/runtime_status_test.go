/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestRuntimeNetworkReportsTheEffectiveMode(t *testing.T) {
	if got := runtimeNetwork(types.Override{HostNetwork: true}); got != "host" {
		t.Fatalf("host network: %s", got)
	}
	if got := runtimeNetwork(types.Override{Network: true}); got != "isolated" {
		t.Fatalf("isolated network: %s", got)
	}
	if got := runtimeNetwork(types.Override{}); got != "none" {
		t.Fatalf("disabled network: %s", got)
	}
}

func TestFilterRuntimeStatusesAcceptsServiceName(t *testing.T) {
	statuses := []RuntimeStatus{{Origin: "github.com/example/server", Instance: "service-api", Service: "api"}}
	filtered, err := FilterRuntimeStatuses(statuses, "github.com/example/server", "api")
	if err != nil || len(filtered) != 1 {
		t.Fatalf("filtered: %#v, %v", filtered, err)
	}
}

func TestRuntimeHealthFailsForAnUnhealthyProcess(t *testing.T) {
	err := RuntimeHealthError([]RuntimeStatus{{ProcessState: "running", Health: "unhealthy"}})
	var exitErr *types.ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Fatalf("health error: %v", err)
	}
	if err = RuntimeHealthError([]RuntimeStatus{{ProcessState: "running", Health: "healthy"}}); err != nil {
		t.Fatal(err)
	}
	if err = RuntimeHealthError([]RuntimeStatus{{ProcessState: "running", Health: "none"}}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedStatusPrefersTheRunningContainer(t *testing.T) {
	running := RuntimeStatus{ContainerState: "running", Since: time.Now().Add(-time.Hour)}
	stopped := RuntimeStatus{ContainerState: "stopped", Since: time.Now()}
	if !preferRuntimeStatus(running, stopped) {
		t.Fatal("a stopped stale container was preferred over the running container")
	}
	if preferRuntimeStatus(stopped, running) {
		t.Fatal("a stopped stale container replaced the running container")
	}
}

func TestParseListeningPortsFiltersByProcessSocket(t *testing.T) {
	data := []byte(`  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 42
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 99
   2: 0100007F:2328 0100007F:1234 01 00000000:00000000 00:00000000 00000000 1000 0 42
`)
	ports := parseListeningPorts(data, map[string]bool{"42": true})
	if len(ports) != 1 || ports[0] != 3000 {
		t.Fatalf("listening ports: %v", ports)
	}
}

func TestListeningPortsForProcessReadsProc(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	want := listener.Addr().(*net.TCPAddr).Port
	for _, port := range listeningPortsForProcess(os.Getpid()) {
		if port == want {
			return
		}
	}
	t.Fatalf("port %d was not found", want)
}
