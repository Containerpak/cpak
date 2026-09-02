/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/shirou/gopsutil/process"
	"golang.org/x/sys/unix"
)

func processNamespace(pid int) (uint64, uint64, error) {
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid), "ns", "pid"))
	if err != nil {
		return 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("process namespace identity is unavailable")
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func (c *Cpak) EnvironmentProcesses(value string) ([]types.EnvironmentProcess, error) {
	_, container, err := c.environmentContainer(value)
	if err != nil {
		return nil, err
	}
	device, inode, err := processNamespace(container.Pid)
	if err != nil {
		return nil, err
	}
	hostDevice, hostInode, err := processNamespace(os.Getpid())
	if err != nil {
		return nil, err
	}
	if device == hostDevice && inode == hostInode {
		return nil, errors.New("process management is unavailable when the host process namespace is shared")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := []types.EnvironmentProcess{}
	for _, entry := range entries {
		pid, processID, ok := parseProcessID(entry.Name())
		if !ok {
			continue
		}
		candidateDevice, candidateInode, statErr := processNamespace(pid)
		if statErr != nil || candidateDevice != device || candidateInode != inode {
			continue
		}
		current, processErr := process.NewProcess(processID)
		if processErr != nil {
			continue
		}
		status, _ := current.Status()
		command, _ := current.Cmdline()
		if command == "" {
			command, _ = current.Name()
		}
		cpu, _ := current.CPUPercent()
		memory := uint64(0)
		if info, memoryErr := current.MemoryInfo(); memoryErr == nil && info != nil {
			memory = info.RSS
		}
		result = append(result, types.EnvironmentProcess{PID: processID, Command: command, CPU: cpu, Memory: memory, CanSignal: pid != container.Pid && status != "Z"})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].PID < result[right].PID })
	return result, nil
}

func parseProcessID(value string) (int, int32, bool) {
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed <= 0 {
		return 0, 0, false
	}
	return int(parsed), int32(parsed), true
}

var environmentSignals = map[string]unix.Signal{
	"HUP": unix.SIGHUP, "INT": unix.SIGINT, "KILL": unix.SIGKILL,
	"TERM": unix.SIGTERM, "USR1": unix.SIGUSR1, "USR2": unix.SIGUSR2,
	"STOP": unix.SIGSTOP, "CONT": unix.SIGCONT,
}

func EnvironmentSignalNames() []string {
	names := make([]string, 0, len(environmentSignals))
	for name := range environmentSignals {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Cpak) SignalEnvironmentProcess(value string, pid int, name string) error {
	_, container, err := c.environmentContainer(value)
	if err != nil {
		return err
	}
	if pid <= 0 || pid == container.Pid {
		return errors.New("the environment init process cannot be signalled here")
	}
	signal, ok := environmentSignals[strings.ToUpper(strings.TrimPrefix(name, "SIG"))]
	if !ok {
		return fmt.Errorf("unsupported signal %q", name)
	}
	device, inode, err := processNamespace(container.Pid)
	if err != nil {
		return err
	}
	hostDevice, hostInode, err := processNamespace(os.Getpid())
	if err != nil {
		return err
	}
	if device == hostDevice && inode == hostInode {
		return errors.New("process management is unavailable when the host process namespace is shared")
	}
	targetDevice, targetInode, err := processNamespace(pid)
	if err != nil || targetDevice != device || targetInode != inode {
		return errors.New("process does not belong to this environment")
	}
	started, err := processStartTime(pid)
	if err != nil {
		return err
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return fmt.Errorf("open process handle: %w", err)
	}
	defer unix.Close(pidfd)
	currentDevice, currentInode, err := processNamespace(pid)
	currentStarted, startErr := processStartTime(pid)
	if err != nil || startErr != nil || currentDevice != device || currentInode != inode || currentStarted != started {
		return errors.New("process changed while it was being verified")
	}
	if err = unix.PidfdSendSignal(pidfd, signal, nil, 0); err != nil {
		return fmt.Errorf("send signal: %w", err)
	}
	return nil
}
