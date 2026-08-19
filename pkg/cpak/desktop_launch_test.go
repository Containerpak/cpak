/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"github.com/mirkobrombin/cpak/pkg/grantproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestPrepareDesktopLaunchArgumentsMountsAFileURI(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "document with spaces.html")
	if err := os.WriteFile(selected, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := tempSocketPath(t)
	listener, err := net.Listen("unixpacket", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mounted := make(chan filegrant.Grant, 1)
	failed := make(chan error, 1)
	go func() {
		accepted, acceptErr := listener.Accept()
		if acceptErr != nil {
			failed <- acceptErr
			return
		}
		connection := accepted.(*net.UnixConn)
		defer connection.Close()
		request, sources, receiveErr := grantproto.Receive(connection)
		if receiveErr != nil {
			failed <- receiveErr
			return
		}
		defer sources.Close()
		mounted <- request.Grant
		failed <- grantproto.Reply(connection, grantproto.Response{Target: request.Grant.Target})
	}()
	original := (&url.URL{Scheme: "file", Host: "localhost", Path: selected, Fragment: "preview"}).String()
	cp := Cpak{}
	if err := cp.SetDesktopFileSpan("0,0"); err != nil {
		t.Fatal(err)
	}
	arguments, err := cp.prepareDesktopLaunchArguments("github.com/example/browser", nil, types.Container{GrantSocketPath: socket}, []string{original})
	if err != nil {
		t.Fatal(err)
	}
	grant := <-mounted
	if err = <-failed; err != nil {
		t.Fatal(err)
	}
	if grant.Source != selected || grant.Access != filegrant.AccessReadOnly || grant.Lifetime != filegrant.LifetimeSession {
		t.Fatalf("grant: %+v", grant)
	}
	parsed, err := url.Parse(arguments[0])
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.Path != grant.Target || parsed.Fragment != "preview" {
		t.Fatalf("rewritten URI: %s", arguments[0])
	}
}

func TestPrepareDesktopLaunchArgumentsUsesAnExistingHostScope(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(selected, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	cp := Cpak{}
	if err := cp.SetDesktopFileSpan("0,0"); err != nil {
		t.Fatal(err)
	}
	arguments, err := cp.prepareDesktopLaunchArguments("github.com/example/viewer", []types.FilesystemPermission{{Path: "host", Access: "read-only"}}, types.Container{}, []string{selected})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/run/host", selected)
	if arguments[0] != want {
		t.Fatalf("rewritten path: got %s, want %s", arguments[0], want)
	}
}

func TestPrepareDesktopLaunchArgumentsLeavesOtherArgumentsAlone(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	arguments := []string{"/usr/bin/internal-tool", "--open", "https://example.com/file", missing, "file://remote.example/share/file"}
	cp := Cpak{}
	if err := cp.SetDesktopFileSpan("2,0"); err != nil {
		t.Fatal(err)
	}
	rewritten, err := cp.prepareDesktopLaunchArguments("github.com/example/browser", nil, types.Container{}, arguments)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/internal-tool", "--open", "https://example.com/file", missing, "file://remote.example/share/file"}
	if len(rewritten) != len(want) {
		t.Fatalf("rewritten arguments: got %v, want %v", rewritten, want)
	}
	for index := range want {
		if rewritten[index] != want[index] {
			t.Fatalf("argument %d changed: got %s, want %s", index, rewritten[index], want[index])
		}
	}
}
