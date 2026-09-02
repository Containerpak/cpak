/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jezek/xgb"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/cpak/pkg/x11bridge"
)

const x11BrokerStartupTimeout = 5 * time.Second

func startX11Broker(container types.Container, clipboard types.ClipboardGrant) (types.Container, error) {
	executable, err := os.Executable()
	if err != nil {
		return container, fmt.Errorf("find cpak binary for X11 broker: %w", err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		return container, fmt.Errorf("create X11 broker readiness pipe: %w", err)
	}
	defer readyReader.Close()
	arguments := []string{
		"x11-broker",
		"--nested-display", x11BrokerDisplay(container),
		"--nested-authority", container.X11AuthorityPath,
		"--server-pid", strconv.Itoa(container.X11BridgePid),
		"--server-start-time", strconv.FormatUint(container.X11BridgeStartTime, 10),
		"--container-pid", strconv.Itoa(container.Pid),
		"--container-start-time", strconv.FormatUint(container.ProcessStartTime, 10),
		"--container-id", container.CpakId,
		"--ready-fd", "3",
	}
	if container.X11HostWindowName != "" {
		arguments = append(arguments, "--host-display", os.Getenv("DISPLAY"), "--host-window", container.X11HostWindowName)
	}
	if clipboard.HostToApp {
		arguments = append(arguments, "--host-to-app")
	}
	if clipboard.AppToHost {
		arguments = append(arguments, "--app-to-host")
	}
	command := exec.Command(executable, arguments...)
	command.ExtraFiles = []*os.File{readyWriter}
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logFile, err := os.OpenFile(container.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		readyWriter.Close()
		return container, fmt.Errorf("open X11 broker log: %w", err)
	}
	defer logFile.Close()
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		readyWriter.Close()
		return container, fmt.Errorf("start X11 broker: %w", err)
	}
	_ = readyWriter.Close()
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	ready := make(chan error, 1)
	go func() {
		buffer := []byte{0}
		_, readErr := io.ReadFull(readyReader, buffer)
		if readErr == nil && buffer[0] != 1 {
			readErr = errors.New("invalid X11 broker readiness response")
		}
		ready <- readErr
	}()
	timer := time.NewTimer(x11BrokerStartupTimeout)
	defer timer.Stop()
	select {
	case err = <-ready:
		if err != nil {
			_ = command.Process.Kill()
			<-exited
			return container, fmt.Errorf("start X11 broker: %w", err)
		}
	case err = <-exited:
		if err == nil {
			err = errors.New("X11 broker exited before readiness")
		}
		return container, fmt.Errorf("start X11 broker: %w", err)
	case <-timer.C:
		_ = command.Process.Kill()
		<-exited
		return container, errors.New("X11 broker readiness timed out")
	}
	container.X11BrokerPid = command.Process.Pid
	container.X11BrokerStartTime, err = processStartTime(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		<-exited
		return container, fmt.Errorf("identify X11 broker: %w", err)
	}
	container.X11BrokerRequired = true
	return container, nil
}

type X11BrokerOptions struct {
	NestedDisplay      string
	NestedAuthority    string
	HostDisplay        string
	HostWindow         string
	ServerPid          int
	ServerStartTime    uint64
	ContainerPid       int
	ContainerStartTime uint64
	ContainerID        string
	ReadyFD            int
	HostToApp          bool
	AppToHost          bool
}

func RunX11Broker(options X11BrokerOptions) error {
	if !filepath.IsAbs(options.NestedDisplay) || !filepath.IsAbs(options.NestedAuthority) || options.ServerPid <= 0 || options.ServerStartTime == 0 || options.ContainerPid <= 0 || options.ContainerStartTime == 0 || options.ContainerID == "" || options.ReadyFD < 3 {
		return errors.New("invalid X11 broker configuration")
	}
	ready := os.NewFile(uintptr(options.ReadyFD), "x11-broker-ready")
	if ready == nil {
		return errors.New("X11 broker readiness descriptor is unavailable")
	}
	defer ready.Close()
	var host *xgb.Conn
	var err error
	if options.HostWindow != "" {
		if options.HostDisplay == "" {
			return errors.New("host X11 display is required for a nested host window")
		}
		host, err = xgb.NewConnDisplay(options.HostDisplay)
		if err != nil {
			return fmt.Errorf("connect to host X11 display: %w", err)
		}
		defer host.Close()
	}
	previousAuthority, hadAuthority := os.LookupEnv("XAUTHORITY")
	if err = os.Setenv("XAUTHORITY", options.NestedAuthority); err != nil {
		return fmt.Errorf("select isolated X11 authority: %w", err)
	}
	nested, err := xgb.NewConnDisplay(options.NestedDisplay)
	if hadAuthority {
		_ = os.Setenv("XAUTHORITY", previousAuthority)
	} else {
		_ = os.Unsetenv("XAUTHORITY")
	}
	if err != nil {
		return fmt.Errorf("connect to isolated X11 display: %w", err)
	}
	defer nested.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	container := types.Container{CpakId: options.ContainerID, Pid: options.ContainerPid, ProcessStartTime: options.ContainerStartTime}
	var stop sync.Once
	stopContainer := func() {
		stop.Do(func() {
			if sameContainerProcess(container, options.ContainerPid) {
				_ = syscall.Kill(options.ContainerPid, syscall.SIGTERM)
			}
			if sameRecordedProcess(options.ServerPid, options.ServerStartTime) {
				_ = syscall.Kill(options.ServerPid, syscall.SIGTERM)
			}
		})
	}
	return x11bridge.Run(ctx, x11bridge.Options{
		Nested: nested, Host: host, HostWindow: options.HostWindow,
		HostToApp: options.HostToApp, AppToHost: options.AppToHost,
		ServerAlive:   func() bool { return sameRecordedProcess(options.ServerPid, options.ServerStartTime) },
		StopContainer: stopContainer,
		Ready: func() error {
			_, writeErr := ready.Write([]byte{1})
			return writeErr
		},
	})
}
