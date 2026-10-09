/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/sandbox"
	"github.com/mirkobrombin/cpak/pkg/unixsocket"
)

type ApplicationRuntimeCmd struct{}

type applicationRuntimeConfig struct {
	UserNamespaces bool
	AllowPtrace    bool
	Env            []string
	Grants         []sandbox.PathGrant
	IdleTimeout    time.Duration
}

func (c *SpawnCmd) startApplicationRuntime(listener *unixsocket.Listener, env []string, grants []sandbox.PathGrant, idleTimeout time.Duration, reaper *childReaper) error {
	fd, err := listener.File()
	if err != nil {
		return fmt.Errorf("copy runtime listener: %w", err)
	}
	defer fd.Close()
	configRead, configWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer configRead.Close()
	defer configWrite.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	// All launches share this nested user namespace. Init and its file grant
	// worker stay in the parent namespace, outside the application's authority.
	command := c.applicationCommand([]string{"application-runtime"}, env)
	command.ExtraFiles = []*os.File{configRead, fd, readyWrite}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err = reaper.start(command, command.Start); err != nil {
		return fmt.Errorf("start application runtime: %w", err)
	}
	configRead.Close()
	readyWrite.Close()
	config := applicationRuntimeConfig{c.UserNamespaces, c.AllowPtrace, env, grants, idleTimeout}
	err = json.NewEncoder(configWrite).Encode(config)
	configWrite.Close()
	if err == nil {
		err = readyRead.SetReadDeadline(time.Now().Add(30 * time.Second))
	}
	if err == nil {
		var ready [1]byte
		_, err = io.ReadFull(readyRead, ready[:])
		if err == nil && ready[0] != 1 {
			err = fmt.Errorf("invalid application runtime readiness")
		}
	}
	if err == nil {
		err = c.signalReady()
	}
	if err != nil {
		_ = command.Process.Kill()
		_ = reaper.wait(command)
		return fmt.Errorf("prepare application runtime: %w", err)
	}
	return reaper.wait(command)
}

func (c *ApplicationRuntimeCmd) Run() error {
	configFile := os.NewFile(3, "runtime-config")
	listenerFile := os.NewFile(4, "runtime-listener")
	readyFile := os.NewFile(5, "runtime-ready")
	defer configFile.Close()
	defer listenerFile.Close()
	defer readyFile.Close()
	var config applicationRuntimeConfig
	if err := json.NewDecoder(configFile).Decode(&config); err != nil {
		return fmt.Errorf("read application runtime configuration: %w", err)
	}
	configFile.Close()
	listener, err := net.FileListener(listenerFile)
	if err != nil {
		return fmt.Errorf("restore runtime listener: %w", err)
	}
	listenerFile.Close()
	defer listener.Close()
	unixListener, ok := listener.(*net.UnixListener)
	if !ok {
		return fmt.Errorf("application runtime requires a Unix listener")
	}
	unixListener.SetUnlinkOnClose(false)
	if _, err = readyFile.Write([]byte{1}); err != nil {
		return fmt.Errorf("signal application runtime readiness: %w", err)
	}
	readyFile.Close()
	spawn := &SpawnCmd{UserNamespaces: config.UserNamespaces, AllowPtrace: config.AllowPtrace}
	return spawn.serveRuntime(unixListener, config.Env, config.Grants, config.IdleTimeout)
}

func runtimeLaunchCommand(args, env []string) *exec.Cmd {
	command := exec.Command(cpakInContainerPath, args...)
	command.Env = env
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}
