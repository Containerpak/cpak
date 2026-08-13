/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"os"
	"os/exec"
	"syscall"
)

type namespaceOptions struct {
	IsolateNetwork bool
	ShareProcesses bool
	IsolateCgroup  bool
}

func nativeNamespaceCommand(path string, args []string, options namespaceOptions) *exec.Cmd {
	flags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC)
	if options.IsolateNetwork {
		flags |= syscall.CLONE_NEWNET
	}
	if !options.ShareProcesses {
		flags |= syscall.CLONE_NEWPID
	}
	if options.IsolateCgroup {
		flags |= syscall.CLONE_NEWCGROUP
	}

	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: flags,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
		Setsid:                     true,
	}
	return cmd
}
