/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/logger"
)

const (
	slirpNameserver       = "10.0.2.3"
	networkResolverPath   = "/etc/resolv.conf"
	networkRefreshPeriod  = time.Second
	networkStartupTimeout = 20 * time.Second
)

var defaultSlirpPath string

type userNetworkPlan struct {
	path string
}

func resolveUserNetwork(enabled, hostNetwork bool) (*userNetworkPlan, error) {
	if hostNetwork {
		if !enabled {
			return nil, fmt.Errorf("host network requires network access")
		}
		return nil, nil
	}
	if !enabled {
		return nil, nil
	}
	path := defaultSlirpPath
	if path == "" {
		path = "slirp4netns"
	}
	path, err := exec.LookPath(path)
	if err != nil {
		return nil, fmt.Errorf("network access requires slirp4netns: %w", err)
	}
	return &userNetworkPlan{path: path}, nil
}

func (p *userNetworkPlan) command(pid int, ready, exit *os.File) *exec.Cmd {
	command := exec.Command(p.path,
		"--configure",
		"--disable-host-loopback",
		"--enable-sandbox",
		"--enable-seccomp",
		"--ready-fd=3",
		"--exit-fd=4",
		strconv.Itoa(pid),
		"tap0",
	)
	command.ExtraFiles = []*os.File{ready, exit}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command
}

func (p *userNetworkPlan) supervisorCommand(cpakBinary string, pid int, ready, exit *os.File) *exec.Cmd {
	command := exec.Command(cpakBinary,
		"network-helper",
		"--slirp-path", p.path,
		"--namespace-pid", strconv.Itoa(pid),
		"--ready-fd=3",
		"--exit-fd=4",
	)
	command.ExtraFiles = []*os.File{ready, exit}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command
}

func readNetworkReady(reader io.Reader) error {
	buffer := []byte{0}
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return err
	}
	if buffer[0] != 1 && buffer[0] != '1' {
		return fmt.Errorf("invalid network readiness response")
	}
	return nil
}

type userNetworkProcess struct {
	command *exec.Cmd
	exited  <-chan error
}

type userNetworkSupervisor struct {
	plan         *userNetworkPlan
	namespacePID int
	resolverPath string
	period       time.Duration
}

func RunUserNetworkHelper(slirpPath string, namespacePID, readyFD, exitFD int) error {
	if slirpPath == "" || namespacePID <= 0 || readyFD < 3 || exitFD < 3 {
		return errors.New("invalid userspace network helper configuration")
	}
	ready := os.NewFile(uintptr(readyFD), "network-ready")
	exit := os.NewFile(uintptr(exitFD), "network-exit")
	if ready == nil || exit == nil {
		if ready != nil {
			ready.Close()
		}
		if exit != nil {
			exit.Close()
		}
		return errors.New("userspace network helper descriptors are unavailable")
	}
	defer ready.Close()
	defer exit.Close()

	supervisor := userNetworkSupervisor{
		plan:         &userNetworkPlan{path: slirpPath},
		namespacePID: namespacePID,
		resolverPath: networkResolverPath,
		period:       networkRefreshPeriod,
	}
	return supervisor.run(ready, exit)
}

func (s userNetworkSupervisor) run(ready, exit *os.File) error {
	lifecycle := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, exit)
		close(lifecycle)
	}()

	fingerprint, fingerprintErr := networkResolverFingerprint(s.resolverPath)
	process, err := s.start(exit)
	if err != nil {
		return err
	}
	if _, err = ready.Write([]byte{1}); err != nil {
		stopUserNetworkProcess(process)
		return fmt.Errorf("report userspace network readiness: %w", err)
	}

	ticker := time.NewTicker(s.period)
	defer ticker.Stop()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	for {
		select {
		case <-lifecycle:
			stopUserNetworkProcess(process)
			return nil
		case <-signals:
			stopUserNetworkProcess(process)
			return nil
		case err = <-process.exited:
			var alive bool
			process, alive = s.restart(exit, lifecycle, signals, err)
			if !alive {
				return nil
			}
		case <-ticker.C:
			current, readErr := networkResolverFingerprint(s.resolverPath)
			if readErr != nil || (fingerprintErr == nil && current == fingerprint) {
				continue
			}
			stopUserNetworkProcess(process)
			process, err = s.start(exit)
			if err != nil {
				var alive bool
				process, alive = s.restart(exit, lifecycle, signals, err)
				if !alive {
					return nil
				}
			}
			fingerprint = current
			fingerprintErr = nil
		}
	}
}

func (s userNetworkSupervisor) restart(exit *os.File, lifecycle <-chan struct{}, signals <-chan os.Signal, cause error) (*userNetworkProcess, bool) {
	for {
		if cause == nil {
			cause = errors.New("userspace network helper exited")
		}
		logger.Printf("Warning: %v; retrying", cause)
		timer := time.NewTimer(s.period)
		select {
		case <-lifecycle:
			timer.Stop()
			return nil, false
		case <-signals:
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		process, err := s.start(exit)
		if err == nil {
			return process, true
		}
		cause = fmt.Errorf("restart userspace network helper: %w", err)
	}
}

func (s userNetworkSupervisor) start(exit *os.File) (*userNetworkProcess, error) {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create userspace network readiness pipe: %w", err)
	}
	command := s.plan.command(s.namespacePID, readyWriter, exit)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = command.Start(); err != nil {
		readyReader.Close()
		readyWriter.Close()
		return nil, fmt.Errorf("start userspace network helper: %w", err)
	}
	_ = readyWriter.Close()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	readyResult := make(chan error, 1)
	go func() {
		defer readyReader.Close()
		readyResult <- readNetworkReady(readyReader)
	}()
	timeout := time.NewTimer(networkStartupTimeout)
	defer timeout.Stop()
	select {
	case err = <-readyResult:
		if err != nil {
			_ = command.Process.Kill()
			<-exited
			return nil, fmt.Errorf("userspace network helper readiness: %w", err)
		}
		return &userNetworkProcess{command: command, exited: exited}, nil
	case err = <-exited:
		if err == nil {
			err = errors.New("userspace network helper exited before readiness")
		}
		return nil, err
	case <-timeout.C:
		_ = command.Process.Kill()
		<-exited
		return nil, errors.New("userspace network helper readiness timed out")
	}
}

func stopUserNetworkProcess(process *userNetworkProcess) {
	if process == nil || process.command == nil || process.command.Process == nil {
		return
	}
	_ = process.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-process.exited:
		return
	case <-time.After(2 * time.Second):
		_ = process.command.Process.Kill()
		<-process.exited
	}
}

func networkResolverFingerprint(path string) ([sha256.Size]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(content), nil
}
