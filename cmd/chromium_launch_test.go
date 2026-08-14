/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestForwardChromiumSingleton(t *testing.T) {
	profile := t.TempDir()
	remote := t.TempDir()
	socketPath := filepath.Join(remote, "SingletonSocket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Symlink(socketPath, filepath.Join(profile, "SingletonSocket")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("cookie", filepath.Join(profile, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("cookie", filepath.Join(remote, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}

	received := make(chan []byte, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		payload := make([]byte, chromiumSingletonMessageLimit)
		read, _ := connection.Read(payload)
		received <- append([]byte{}, payload[:read]...)
		_, _ = connection.Write([]byte("ACK"))
	}()

	forwarded, err := forwardChromiumSingleton(profile, "/opt/chrome", []string{"https://cpak.it"})
	if err != nil {
		t.Fatal(err)
	}
	if !forwarded {
		t.Fatal("request was not forwarded")
	}
	select {
	case payload := <-received:
		if !bytes.HasSuffix(payload, []byte("\x00/opt/chrome\x00https://cpak.it")) {
			t.Fatalf("unexpected payload %q", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("singleton did not receive the request")
	}
}

func TestChromiumSingletonRejectsMismatchedCookie(t *testing.T) {
	profile := t.TempDir()
	remote := t.TempDir()
	socketPath := filepath.Join(remote, "SingletonSocket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Symlink(socketPath, filepath.Join(profile, "SingletonSocket")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("profile-cookie", filepath.Join(profile, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("remote-cookie", filepath.Join(remote, "SingletonCookie")); err != nil {
		t.Fatal(err)
	}

	forwarded, err := forwardChromiumSingleton(profile, "/opt/chrome", []string{"https://cpak.it"})
	if err != nil {
		t.Fatal(err)
	}
	if forwarded {
		t.Fatal("request crossed a mismatched singleton cookie")
	}
}

func TestChromiumSingletonPayloadLimit(t *testing.T) {
	argument := make([]byte, chromiumSingletonMessageLimit)
	if _, err := chromiumSingletonPayload("/opt/chrome", []string{string(argument)}); err == nil {
		t.Fatal("oversized request was accepted")
	}
}

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := expandUserPath("~/.config/chromium")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(home, ".config", "chromium") {
		t.Fatalf("unexpected expanded path %s", path)
	}
}
