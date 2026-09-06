/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerRestoresServicesInDependencyOrder(t *testing.T) {
	root := t.TempDir()
	store := Store{Directory: filepath.Join(root, "services")}
	database := testDefinition("database")
	database.HealthCommand = "true"
	database.HealthTimeout = 1
	server := testDefinition("server")
	server.DependsOn = []string{"database"}
	if err := store.Put(server); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(database); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(root, "events")
	binary := filepath.Join(root, "cpak-test")
	script := `#!/bin/sh
action="$1"
shift
if [ "$action" = "stop" ]; then
    exit 0
fi
for argument in "$@"; do
    if [ "$argument" = "@sh" ]; then
        exit 0
    fi
done
instance=""
while [ "$#" -gt 0 ]; do
    if [ "$1" = "--instance" ]; then
        instance="$2"
        shift 2
        continue
    fi
    shift
done
printf '%s\n' "$instance" >> "$CPAK_TEST_EVENTS"
trap 'exit 0' TERM INT
while :; do sleep 0.02; done
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_TEST_EVENTS", events)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	manager := Manager{
		Store: store, Binary: binary, SocketPath: filepath.Join(root, "manager.sock"),
		StopTimeout: 100 * time.Millisecond, RefreshInterval: 20 * time.Millisecond,
	}
	go func() { done <- manager.Run(ctx) }()
	waitForTest(t, 3*time.Second, func() bool {
		data, err := os.ReadFile(events)
		return err == nil && len(strings.Fields(string(data))) >= 2
	})
	data, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	started := strings.Fields(string(data))
	if len(started) < 2 || started[0] != "service-database" || started[1] != "service-server" {
		t.Fatalf("start order: %v", started)
	}
	response, err := Send(manager.SocketPath, ControlRequest{Action: "status"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Services) != 2 {
		t.Fatalf("statuses: %#v", response.Services)
	}
	for _, status := range response.Services {
		health := "none"
		if status.Name == "database" {
			health = "healthy"
		}
		if status.State != "running" || status.Health != health || status.PID == 0 || status.Since.IsZero() {
			t.Fatalf("status: %#v", status)
		}
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestManagerRestartsAFailedServiceWithBackoff(t *testing.T) {
	root := t.TempDir()
	store := Store{Directory: filepath.Join(root, "services")}
	definition := testDefinition("server")
	definition.Restart = RestartOnFailure
	if err := store.Put(definition); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(root, "events")
	binary := filepath.Join(root, "cpak-test")
	script := `#!/bin/sh
if [ "$1" = "stop" ]; then
    exit 0
fi
printf 'start\n' >> "$CPAK_TEST_EVENTS"
exit 1
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAK_TEST_EVENTS", events)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	manager := Manager{
		Store: store, Binary: binary, SocketPath: filepath.Join(root, "manager.sock"),
		RestartDelay: 20 * time.Millisecond, RestartLimit: 80 * time.Millisecond,
		StableAfter: time.Second, StopTimeout: 20 * time.Millisecond, RefreshInterval: 5 * time.Millisecond,
	}
	go func() { done <- manager.Run(ctx) }()
	var response ControlResponse
	waitForTest(t, 3*time.Second, func() bool {
		current, err := Send(manager.SocketPath, ControlRequest{Action: "status"}, time.Second)
		if err != nil || len(current.Services) != 1 {
			return false
		}
		response = current
		return current.Services[0].Restarts >= 2 && current.Services[0].LastError != ""
	})
	if len(response.Services) != 1 || response.Services[0].Restarts < 2 || response.Services[0].LastError == "" {
		t.Fatalf("restart status: %#v", response.Services)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
}

func TestManagerReenablesAChangedDefinition(t *testing.T) {
	store := Store{Directory: filepath.Join(t.TempDir(), "services")}
	before := testDefinition("server")
	if err := store.Put(before); err != nil {
		t.Fatal(err)
	}
	runtime := runtimeManager{
		options: Manager{Store: store},
		states: map[string]*runtimeState{
			"server": {definition: before, target: false, restartBlocked: true, restarts: 4, lastError: "failed"},
		},
	}
	after := before
	after.Environment = []string{"MODE=production"}
	if err := store.Put(after); err != nil {
		t.Fatal(err)
	}
	if err := runtime.reload(); err != nil {
		t.Fatal(err)
	}
	state := runtime.states["server"]
	if !state.target || state.restartBlocked || state.restarts != 0 || state.lastError != "" {
		t.Fatalf("reloaded state: %#v", state)
	}
}

func TestManagerSocketUsesAPrivateDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	listener, err := listen(filepath.Join(directory, "manager.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("runtime directory mode: %o", info.Mode().Perm())
	}
}

func waitForTest(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not met")
}
