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
	"golang.org/x/sys/unix"
)

const x11BrokerStartupTimeout = 5 * time.Second

func startX11Broker(container types.Container, clipboard types.ClipboardGrant, runtime *x11BridgeRuntime) (types.Container, error) {
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
		"--container-pid", strconv.Itoa(container.Pid),
		"--container-start-time", strconv.FormatUint(container.ProcessStartTime, 10),
		"--container-id", container.CpakId,
		"--ready-fd", "3",
	}
	if runtime != nil && runtime.lazy {
		arguments = append(arguments, "--listen-fd", "4", "--x11-server", runtime.x11Server)
		if container.WaylandDisplay != "" {
			arguments = append(arguments, "--mixed-wayland")
		}
	} else {
		arguments = append(arguments,
			"--server-pid", strconv.Itoa(container.X11BridgePid),
			"--server-start-time", strconv.FormatUint(container.X11BridgeStartTime, 10),
		)
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
	if runtime != nil && runtime.lazy {
		if runtime.listener == nil {
			readyWriter.Close()
			return container, errors.New("start X11 broker: private listener is unavailable")
		}
		command.ExtraFiles = append(command.ExtraFiles, runtime.listener)
	}
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
	if runtime != nil {
		runtime.close()
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
	if runtime != nil && runtime.lazy {
		container.X11BridgePid = container.X11BrokerPid
		container.X11BridgeStartTime = container.X11BrokerStartTime
	}
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
	ListenFD           int
	X11Server          string
	MixedWayland       bool
	HostToApp          bool
	AppToHost          bool
}

func RunX11Broker(options X11BrokerOptions) error {
	if !filepath.IsAbs(options.NestedDisplay) || !filepath.IsAbs(options.NestedAuthority) || options.ContainerPid <= 0 || options.ContainerStartTime == 0 || options.ContainerID == "" || options.ReadyFD < 3 {
		return errors.New("invalid X11 broker configuration")
	}
	if options.ListenFD != 0 {
		if options.ListenFD < 3 || !filepath.IsAbs(options.X11Server) || options.ServerPid != 0 || options.ServerStartTime != 0 {
			return errors.New("invalid lazy X11 broker configuration")
		}
		return runLazyX11Broker(options)
	}
	if options.ServerPid <= 0 || options.ServerStartTime == 0 || options.X11Server != "" || options.MixedWayland {
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
	nested, err := x11BrokerConnection(options)
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

func x11BrokerConnection(options X11BrokerOptions) (*xgb.Conn, error) {
	previousAuthority, hadAuthority := os.LookupEnv("XAUTHORITY")
	if err := os.Setenv("XAUTHORITY", options.NestedAuthority); err != nil {
		return nil, fmt.Errorf("select isolated X11 authority: %w", err)
	}
	connection, err := xgb.NewConnDisplay(options.NestedDisplay)
	if hadAuthority {
		_ = os.Setenv("XAUTHORITY", previousAuthority)
	} else {
		_ = os.Unsetenv("XAUTHORITY")
	}
	return connection, err
}

func runLazyX11Broker(options X11BrokerOptions) error {
	ready := os.NewFile(uintptr(options.ReadyFD), "x11-broker-ready")
	if ready == nil {
		return errors.New("X11 broker readiness descriptor is unavailable")
	}
	defer ready.Close()
	listener := os.NewFile(uintptr(options.ListenFD), "x11-listener")
	if listener == nil {
		return errors.New("private X11 listener descriptor is unavailable")
	}
	defer listener.Close()
	if _, err := ready.Write([]byte{1}); err != nil {
		return fmt.Errorf("report X11 broker readiness: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	container := types.Container{CpakId: options.ContainerID, Pid: options.ContainerPid, ProcessStartTime: options.ContainerStartTime}
	for sameContainerProcess(container, options.ContainerPid) {
		pending, err := waitForX11Client(ctx, int(listener.Fd()), func() bool {
			return sameContainerProcess(container, options.ContainerPid)
		})
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if err = runLazyX11Display(ctx, listener, container, options); err != nil {
			return err
		}
		if !options.MixedWayland {
			return nil
		}
	}
	return nil
}

func waitForX11Client(ctx context.Context, listener int, containerAlive func() bool) (bool, error) {
	for {
		if ctx.Err() != nil || !containerAlive() {
			return false, nil
		}
		poll := []unix.PollFd{{Fd: int32(listener), Events: unix.POLLIN}}
		count, err := unix.Poll(poll, 100)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return false, fmt.Errorf("wait for X11 client: %w", err)
		}
		if count > 0 {
			if poll[0].Revents&unix.POLLIN != 0 {
				return true, nil
			}
			return false, errors.New("private X11 listener failed")
		}
	}
}

func runLazyX11Display(ctx context.Context, listener *os.File, container types.Container, options X11BrokerOptions) error {
	displayReader, displayWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create X11 display pipe: %w", err)
	}
	defer displayReader.Close()
	command := exec.Command(options.X11Server,
		"-auth", options.NestedAuthority, "-nolisten", "tcp", "-geometry", "1280x800",
		"-listenfd", "3", "-displayfd", "4",
	)
	command.ExtraFiles = []*os.File{listener, displayWriter}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = command.Start(); err != nil {
		displayWriter.Close()
		return fmt.Errorf("start isolated X11 display: %w", err)
	}
	displayWriter.Close()
	serverStart, err := processStartTime(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("identify isolated X11 display: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	stopServer := func() {
		if sameRecordedProcess(command.Process.Pid, serverStart) {
			_ = syscall.Kill(command.Process.Pid, syscall.SIGTERM)
		}
	}
	defer func() {
		stopServer()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			if sameRecordedProcess(command.Process.Pid, serverStart) {
				_ = syscall.Kill(command.Process.Pid, syscall.SIGKILL)
			}
			<-exited
		}
	}()
	_, err = readX11Display(displayReader, 5*time.Second)
	if err != nil {
		return err
	}
	nested, err := x11BrokerConnection(options)
	if err != nil {
		return fmt.Errorf("connect to isolated X11 display: %w", err)
	}
	defer nested.Close()
	var stop sync.Once
	stopDisplay := func() {
		stop.Do(func() {
			if !options.MixedWayland && sameContainerProcess(container, options.ContainerPid) {
				_ = syscall.Kill(options.ContainerPid, syscall.SIGTERM)
			}
			stopServer()
		})
	}
	return x11bridge.Run(ctx, x11bridge.Options{
		Nested: nested, HostToApp: options.HostToApp, AppToHost: options.AppToHost,
		ServerAlive: func() bool {
			return sameRecordedProcess(command.Process.Pid, serverStart) && sameContainerProcess(container, options.ContainerPid)
		},
		StopContainer: stopDisplay,
	})
}
