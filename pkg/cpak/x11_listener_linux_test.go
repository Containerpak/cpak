/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/unixsocket"
)

func TestPrivateX11ListenerAfterLongPathHandoff(t *testing.T) {
	if path := os.Getenv("CPAK_TEST_X11_LISTENER_PATH"); path != "" {
		listener := os.NewFile(3, "x11-listener")
		defer listener.Close()
		if err := validatePrivateX11Listener(listener, path); err != nil {
			t.Fatal(err)
		}
		return
	}
	directory := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, isolatedX11SocketName)
	listener, err := createPrivateX11Listener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestPrivateX11ListenerAfterLongPathHandoff$")
	command.Env = append(os.Environ(), "CPAK_TEST_X11_LISTENER_PATH="+path)
	command.ExtraFiles = []*os.File{listener}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("long-path listener handoff failed: %v\n%s", err, output)
	}
	if err := validatePrivateX11Listener(listener, path); err != nil {
		t.Fatalf("listener did not survive the handoff: %v", err)
	}
}

func TestPrivateX11ListenerRejectsAnotherLongPath(t *testing.T) {
	directory := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	listener, err := createPrivateX11Listener(first)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	other, err := createPrivateX11Listener(second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = validatePrivateX11Listener(listener, second); err == nil {
		t.Fatal("listener accepted another socket path")
	}
}

func TestPrivateX11QueueRejectsAForwardedClient(t *testing.T) {
	directory := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	listener, err := createPrivateX11Listener(first)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	other, err := createPrivateX11Listener(second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	client, err := unixsocket.DialTimeout("unix", first, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	forwarded, err := unixsocket.DialTimeout("unix", second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer forwarded.Close()
	if pending, err := pendingX11Client(listener, client); err != nil || !pending {
		t.Fatalf("original listener did not recognize its client: %v", err)
	}
	if pending, err := pendingX11Client(other, client); err != nil || pending {
		t.Fatalf("forwarded connection accepted as the original client: %v", err)
	}
	if pending, err := pendingX11Client(other, forwarded); err != nil || !pending {
		t.Fatalf("forwarding endpoint did not recognize its client: %v", err)
	}
}
