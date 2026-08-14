/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		SocketPath:     filepath.Join(t.TempDir(), "broker.sock"),
		Token:          "01234567890123456789012345678901",
		AllowNotify:    true,
		AllowOpenURI:   true,
		OpenURICommand: "/usr/bin/true",
		Notify:         func(context.Context, NotificationRequest) error { return nil },
	}
}

func testClient(options Options) Client {
	return Client{SocketPath: options.SocketPath, Token: options.Token}
}

func startBroker(t *testing.T, options Options) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, options) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(options.SocketPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("system broker did not start")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("system broker stopped with error: %v", err)
		}
	})
	return cancel
}

func TestCallUsesOnlyPermittedOperations(t *testing.T) {
	options := testOptions(t)
	startBroker(t, options)
	client := testClient(options)
	if err := client.Notify(context.Background(), NotificationRequest{AppName: "cpak", Summary: "hello", ExpireTimeout: -1}); err != nil {
		t.Fatalf("notification request: %v", err)
	}
	if err := client.OpenURI(context.Background(), OpenURIRequest{URI: "https://usecpak.org"}); err != nil {
		t.Fatalf("URI request: %v", err)
	}
	if err := client.OpenURI(context.Background(), OpenURIRequest{URI: "file:///home/user/private"}); err == nil {
		t.Fatal("file URI was accepted")
	}
	if err := client.call(context.Background(), "exec", map[string]string{"command": "id"}); err == nil {
		t.Fatal("arbitrary operation was accepted")
	}
}

func TestOpenURIReturnsAfterStartingTheDesktopBackend(t *testing.T) {
	backend := filepath.Join(t.TempDir(), "open-uri")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nsleep 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	options := testOptions(t)
	options.OpenURICommand = backend
	startBroker(t, options)
	started := time.Now()
	if err := testClient(options).OpenURI(context.Background(), OpenURIRequest{URI: "https://usecpak.org"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("URI backend blocked the broker for %s", elapsed)
	}
}

func TestCallRejectsWrongToken(t *testing.T) {
	options := testOptions(t)
	startBroker(t, options)
	client := Client{SocketPath: options.SocketPath, Token: "abcdefghijklmnopqrstuvwxyz012345"}
	if err := client.Notify(context.Background(), NotificationRequest{AppName: "cpak", Summary: "hello", ExpireTimeout: -1}); err == nil {
		t.Fatal("wrong token was accepted")
	}
}

func TestServeRejectsUnauthorizedPeer(t *testing.T) {
	options := testOptions(t)
	options.AuthorizePeer = func(*net.UnixConn) error { return errors.New("denied") }
	startBroker(t, options)
	if err := testClient(options).Notify(context.Background(), NotificationRequest{AppName: "cpak", Summary: "hello", ExpireTimeout: -1}); err == nil {
		t.Fatal("unauthorized peer was accepted")
	}
}

func TestCallsAreSafeUnderContention(t *testing.T) {
	options := testOptions(t)
	startBroker(t, options)
	var group sync.WaitGroup
	errs := make(chan error, 32)
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- testClient(options).Notify(context.Background(), NotificationRequest{AppName: "cpak", Summary: "hello", ExpireTimeout: -1})
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent broker request: %v", err)
		}
	}
}

func TestContainerCallStreamsOutputAndExitCode(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{types.HostActionContainersRead: true}
	options.Containers = func(_ context.Context, owner string, capabilities map[string]bool, _ []ContainerPathGrant, request ContainerRequest, stdout, stderr io.Writer) (int, error) {
		if owner != "app-id" || !capabilities[types.HostActionContainersRead] || request.Operation != "ps" {
			t.Fatalf("unexpected container request: %s %v %+v", owner, capabilities, request)
		}
		_, _ = stdout.Write([]byte("out\n"))
		_, _ = stderr.Write([]byte("err\n"))
		return 7, nil
	}
	startBroker(t, options)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	client := testClient(options)
	client.Stdout = &stdout
	client.Stderr = &stderr
	err := client.Containers(context.Background(), ContainerRequest{Operation: "ps"})
	var exitError *types.ExitError
	if !errors.As(err, &exitError) || exitError.Code != 7 {
		t.Fatalf("container exit: %v", err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("container streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDockerShimSelectsDockerBackend(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{types.HostActionContainersRead: true}
	options.Containers = func(_ context.Context, _ string, _ map[string]bool, _ []ContainerPathGrant, request ContainerRequest, _, _ io.Writer) (int, error) {
		if request.Backend != "docker" || request.Operation != "ps" {
			t.Fatalf("unexpected Docker request: %+v", request)
		}
		return 0, nil
	}
	startBroker(t, options)
	if err := InvokeShim(context.Background(), options.SocketPath, options.Token, "docker", []string{"ps"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestPodmanShimKeepsLegacyRequestShape(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{types.HostActionContainersRead: true}
	options.Containers = func(_ context.Context, _ string, _ map[string]bool, _ []ContainerPathGrant, request ContainerRequest, _, _ io.Writer) (int, error) {
		if request.Backend != "" || request.Operation != "ps" {
			t.Fatalf("unexpected Podman request: %+v", request)
		}
		return 0, nil
	}
	startBroker(t, options)
	if err := InvokeShim(context.Background(), options.SocketPath, options.Token, "podman", []string{"ps"}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestContainerCallCancellationReachesProvider(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{types.HostActionContainersRead: true}
	started := make(chan struct{})
	canceled := make(chan struct{})
	options.Containers = func(ctx context.Context, _ string, _ map[string]bool, _ []ContainerPathGrant, _ ContainerRequest, _, _ io.Writer) (int, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return 130, ctx.Err()
	}
	startBroker(t, options)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- testClient(options).Containers(ctx, ContainerRequest{Operation: "ps"})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("container provider did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled client: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("provider context was not canceled")
	}
}

func TestContainerCallRejectsUnknownPayloadFields(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{types.HostActionContainersRead: true}
	startBroker(t, options)
	err := testClient(options).call(context.Background(), ActionContainers, map[string]any{"operation": "ps", "command_line": "id"})
	if err == nil {
		t.Fatal("unknown container payload field was accepted")
	}
}

func TestValidateURIArgs(t *testing.T) {
	for _, value := range []string{"https://example.com", "mailto:user@example.com"} {
		if err := validateOpenURI(OpenURIRequest{URI: value}); err != nil {
			t.Fatalf("valid URI %q: %v", value, err)
		}
	}
	for _, value := range []string{"/tmp/file", "file:///tmp/file", "javascript:alert(1)", "https://example.com\x00bad"} {
		if err := validateOpenURI(OpenURIRequest{URI: value}); err == nil {
			t.Fatalf("invalid URI %q was accepted", value)
		}
	}
}

func TestParseGIOOpen(t *testing.T) {
	request, err := parseGIOOpen([]string{"open", "https://usecpak.org"})
	if err != nil {
		t.Fatal(err)
	}
	if request.URI != "https://usecpak.org" {
		t.Fatalf("URI: got %q", request.URI)
	}
	for _, args := range [][]string{
		{"open"},
		{"open", "https://one.example", "https://two.example"},
		{"mime", "x-scheme-handler/https"},
	} {
		if _, err = parseGIOOpen(args); err == nil {
			t.Fatalf("accepted gio arguments %v", args)
		}
	}
}

func TestServeDoesNotReplaceAnActiveBroker(t *testing.T) {
	options := testOptions(t)
	startBroker(t, options)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := Serve(ctx, options); err == nil {
		t.Fatal("active broker socket was replaced")
	}
}

func TestOptionsRejectUnknownContainerCapability(t *testing.T) {
	options := testOptions(t)
	options.ContainerOwner = "app-id"
	options.ContainerCapabilities = map[string]bool{"host-exec": true}
	if err := options.validate(); err == nil {
		t.Fatal("unknown container capability was accepted")
	}
}

func TestLaunchApplicationUsesCatalogAndNestedDisplay(t *testing.T) {
	runtimeDirectory := t.TempDir()
	if err := os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	displayPath := filepath.Join(runtimeDirectory, "wayland-0")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: displayPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	token := strings.Repeat("a", 64)
	desktopEntry := filepath.Join(t.TempDir(), "demo.desktop")
	called := false
	options := testOptions(t)
	options.AllowHostApplications = true
	options.Applications = map[string]string{token: desktopEntry}
	options.RuntimeDirectory = runtimeDirectory
	options.LaunchApplication = func(_ context.Context, entry string, args, environment []string) error {
		called = true
		if entry != desktopEntry || len(args) != 1 || args[0] != "file:///tmp/demo" {
			t.Fatalf("unexpected launch: %s %v", entry, args)
		}
		if !containsString(environment, "WAYLAND_DISPLAY="+displayPath) {
			t.Fatalf("nested display is missing: %v", environment)
		}
		return nil
	}
	startBroker(t, options)
	client := testClient(options)
	request := LaunchApplicationRequest{ApplicationToken: token, URIs: []string{"file:///tmp/demo"}, Environment: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}}
	if err := client.LaunchApplication(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("host application backend was not called")
	}
	request.ApplicationToken = strings.Repeat("b", 64)
	request.URIs = nil
	if err := client.LaunchApplication(context.Background(), request); err == nil {
		t.Fatal("application outside the catalog was accepted")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
