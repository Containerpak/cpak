/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		SocketPath:     filepath.Join(t.TempDir(), "broker.sock"),
		Token:          "01234567890123456789012345678901",
		AllowNotify:    true,
		AllowOpenURI:   true,
		OpenURICommand: "/usr/bin/true",
		Notify:         func(context.Context, []string) error { return nil },
	}
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
	if err := Call(options.SocketPath, options.Token, OperationNotify, []string{"hello"}); err != nil {
		t.Fatalf("notification request: %v", err)
	}
	if err := Call(options.SocketPath, options.Token, OperationOpenURI, []string{"https://usecpak.org"}); err != nil {
		t.Fatalf("URI request: %v", err)
	}
	if err := Call(options.SocketPath, options.Token, OperationOpenURI, []string{"file:///home/user/private"}); err == nil {
		t.Fatal("file URI was accepted")
	}
	if err := Call(options.SocketPath, options.Token, "exec", []string{"id"}); err == nil {
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
	if err := Call(options.SocketPath, options.Token, OperationOpenURI, []string{"https://usecpak.org"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("URI backend blocked the broker for %s", elapsed)
	}
}

func TestCallRejectsWrongToken(t *testing.T) {
	options := testOptions(t)
	startBroker(t, options)
	if err := Call(options.SocketPath, "abcdefghijklmnopqrstuvwxyz012345", OperationNotify, []string{"hello"}); err == nil {
		t.Fatal("wrong token was accepted")
	}
}

func TestServeRejectsUnauthorizedPeer(t *testing.T) {
	options := testOptions(t)
	options.AuthorizePeer = func(*net.UnixConn) error { return errors.New("denied") }
	startBroker(t, options)
	if err := Call(options.SocketPath, options.Token, OperationNotify, []string{"hello"}); err == nil {
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
			errs <- Call(options.SocketPath, options.Token, OperationNotify, []string{"hello"})
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

func TestValidateURIArgs(t *testing.T) {
	for _, value := range []string{"https://example.com", "mailto:user@example.com"} {
		if err := validateURIArgs([]string{value}); err != nil {
			t.Fatalf("valid URI %q: %v", value, err)
		}
	}
	for _, value := range []string{"/tmp/file", "file:///tmp/file", "javascript:alert(1)", "https://example.com\x00bad"} {
		if err := validateURIArgs([]string{value}); err == nil {
			t.Fatalf("invalid URI %q was accepted", value)
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
