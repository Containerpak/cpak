/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"os"
	"syscall"
	"testing"
)

func TestNativeNamespaceCommandUsesRootlessMappings(t *testing.T) {
	cmd := nativeNamespaceCommand("/bin/true", nil, namespaceOptions{
		IsolateNetwork: true,
		ShareProcesses: false,
		IsolateCgroup:  true,
	})

	want := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWIPC | syscall.CLONE_NEWNET | syscall.CLONE_NEWPID | syscall.CLONE_NEWCGROUP)
	if cmd.SysProcAttr.Cloneflags != want {
		t.Fatalf("clone flags: got %#x, want %#x", cmd.SysProcAttr.Cloneflags, want)
	}
	if len(cmd.SysProcAttr.UidMappings) != 1 || cmd.SysProcAttr.UidMappings[0].HostID != os.Getuid() {
		t.Fatalf("unexpected uid mappings: %v", cmd.SysProcAttr.UidMappings)
	}
	if len(cmd.SysProcAttr.GidMappings) != 1 || cmd.SysProcAttr.GidMappings[0].HostID != os.Getgid() {
		t.Fatalf("unexpected gid mappings: %v", cmd.SysProcAttr.GidMappings)
	}
	if cmd.SysProcAttr.GidMappingsEnableSetgroups {
		t.Fatal("setgroups must stay disabled for an unprivileged mapping")
	}
}

func TestNativeNamespaceCommandCanShareNetworkAndProcesses(t *testing.T) {
	cmd := nativeNamespaceCommand("/bin/true", nil, namespaceOptions{
		IsolateNetwork: false,
		ShareProcesses: true,
		IsolateCgroup:  false,
	})

	for _, flag := range []uintptr{syscall.CLONE_NEWNET, syscall.CLONE_NEWPID, syscall.CLONE_NEWCGROUP} {
		if cmd.SysProcAttr.Cloneflags&flag != 0 {
			t.Fatalf("unexpected clone flag %#x in %#x", flag, cmd.SysProcAttr.Cloneflags)
		}
	}
	for _, flag := range []uintptr{syscall.CLONE_NEWUSER, syscall.CLONE_NEWNS, syscall.CLONE_NEWUTS, syscall.CLONE_NEWIPC} {
		if cmd.SysProcAttr.Cloneflags&flag == 0 {
			t.Fatalf("missing clone flag %#x in %#x", flag, cmd.SysProcAttr.Cloneflags)
		}
	}
}
