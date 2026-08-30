/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

const subordinateIDRangeSize = 1 << 16

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

	attributes := &syscall.SysProcAttr{
		Cloneflags:                 flags,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		Setsid:                     true,
	}
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = attributes
	return cmd
}

func systemIDNamespaceCommand(path string, args []string, options namespaceOptions) (*exec.Cmd, error) {
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("identify current user: %w", err)
	}
	if _, err = readSubordinateIDRange("/etc/subuid", current.Username, os.Getuid()); err != nil {
		return nil, err
	}
	if _, err = readSubordinateIDRange("/etc/subgid", current.Username, os.Getuid()); err != nil {
		return nil, err
	}
	unshareArgs := []string{
		"--map-auto",
		"--map-root-user",
		"--setgroups=allow",
		"--keep-caps",
		"--mount",
		"--uts",
		"--ipc",
	}
	if options.IsolateNetwork {
		unshareArgs = append(unshareArgs, "--net")
	}
	if !options.ShareProcesses {
		unshareArgs = append(unshareArgs, "--pid", "--fork", "--kill-child=SIGKILL")
	}
	if options.IsolateCgroup {
		unshareArgs = append(unshareArgs, "--cgroup")
	}
	unshareArgs = append(unshareArgs, "--", path)
	unshareArgs = append(unshareArgs, args...)
	return exec.Command("/usr/bin/unshare", unshareArgs...), nil
}

type subordinateIDRange struct {
	start int
	size  int
}

func readSubordinateIDRange(path, username string, id int) (subordinateIDRange, error) {
	file, err := os.Open(path)
	if err != nil {
		return subordinateIDRange{}, err
	}
	defer file.Close()
	numericID := strconv.Itoa(id)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) != 3 || parts[0] != username && parts[0] != numericID {
			continue
		}
		start, startErr := strconv.Atoi(parts[1])
		size, sizeErr := strconv.Atoi(parts[2])
		if startErr == nil && sizeErr == nil && start > 0 && size >= subordinateIDRangeSize {
			return subordinateIDRange{start: start, size: size}, nil
		}
	}
	if err = scanner.Err(); err != nil {
		return subordinateIDRange{}, err
	}
	return subordinateIDRange{}, fmt.Errorf("%s does not assign at least %d IDs to %s", path, subordinateIDRangeSize, username)
}
