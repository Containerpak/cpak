/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package grantproto

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
)

func TestGrantDescriptorRoundTrip(t *testing.T) {
	directory := t.TempDir()
	selected := filepath.Join(directory, "setup.exe")
	if err := os.WriteFile(selected, []byte("MZ"), 0600); err != nil {
		t.Fatal(err)
	}
	grant, err := filegrant.Resolve("github.com/example/app", selected, filegrant.AccessReadOnly, filegrant.LifetimeSession, false)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "grant.sock")
	packet, err := net.Listen("unixpacket", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer packet.Close()
	done := make(chan error, 1)
	go func() {
		accepted, acceptErr := packet.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		connection := accepted.(*net.UnixConn)
		defer connection.Close()
		request, sources, receiveErr := Receive(connection)
		if receiveErr == nil {
			defer sources.Close()
			content := make([]byte, 2)
			_, receiveErr = sources.Selected.ReadAt(content, 0)
			if receiveErr == nil && string(content) != "MZ" {
				receiveErr = os.ErrInvalid
			}
			if receiveErr == nil && request.Grant != grant {
				receiveErr = os.ErrInvalid
			}
		}
		if receiveErr == nil {
			receiveErr = Reply(connection, Response{Target: grant.Target})
		}
		done <- receiveErr
	}()
	file, err := os.Open(selected)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	target, err := Send(socket, grant, file, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if target != grant.Target {
		t.Fatalf("target: got %s, want %s", target, grant.Target)
	}
}
