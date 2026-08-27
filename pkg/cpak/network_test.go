/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"slices"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestNetworkHelperIsOptional(t *testing.T) {
	t.Setenv("PATH", "")
	plan, err := resolveUserNetwork(false)
	if err != nil {
		t.Fatalf("resolve disabled network: %v", err)
	}
	if plan != nil {
		t.Fatalf("got a helper for disabled network: %+v", plan)
	}
	if _, err = resolveUserNetwork(true); err == nil {
		t.Fatal("enabled network started without a userspace network helper")
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
}
