/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

const chromiumSingletonMessageLimit = 32 * 1024

type ChromiumLaunchCmd struct {
	UserDataDir string   `cli:"user-data-dir" help:"Chromium user data directory"`
	Executable  string   `cli:"executable" help:"Chromium executable"`
	ExtraArgs   []string `arg:"extra" help:"Arguments passed to Chromium"`

	cli.Base
}

func (c *ChromiumLaunchCmd) Run() error {
	userDataDir, err := expandUserPath(c.UserDataDir)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(userDataDir) {
		return errors.New("Chromium user data directory must be absolute")
	}
	if !filepath.IsAbs(c.Executable) {
		return errors.New("Chromium executable must be absolute")
	}
	forwarded, err := forwardChromiumSingleton(userDataDir, c.Executable, c.ExtraArgs)
	if err != nil {
		return err
	}
	if forwarded {
		return nil
	}
	return syscall.Exec(c.Executable, append([]string{c.Executable}, c.ExtraArgs...), os.Environ())
}

func expandUserPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}

func forwardChromiumSingleton(userDataDir, executable string, args []string) (bool, error) {
	socketPath, valid, err := chromiumSingletonSocket(userDataDir)
	if err != nil || !valid {
		return false, nil
	}
	payload, err := chromiumSingletonPayload(executable, args)
	if err != nil {
		return false, err
	}
	connection, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
	if err != nil {
		return false, nil
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return false, err
	}
	if _, err = connection.Write(payload); err != nil {
		return false, nil
	}
	if unixConnection, ok := connection.(*net.UnixConn); ok {
		if err = unixConnection.CloseWrite(); err != nil {
			return false, nil
		}
	}
	ack := make([]byte, len("SHUTDOWN"))
	read, err := connection.Read(ack)
	if err != nil {
		return false, nil
	}
	return string(ack[:read]) == "ACK", nil
}

func chromiumSingletonSocket(userDataDir string) (string, bool, error) {
	socketLink := filepath.Join(userDataDir, "SingletonSocket")
	socketPath, err := os.Readlink(socketLink)
	if err != nil {
		return "", false, nil
	}
	if !filepath.IsAbs(socketPath) {
		socketPath = filepath.Join(userDataDir, socketPath)
	}
	socketPath = filepath.Clean(socketPath)
	if filepath.Dir(socketPath) == filepath.Clean(userDataDir) {
		return "", false, nil
	}
	profileCookie, err := os.Readlink(filepath.Join(userDataDir, "SingletonCookie"))
	if err != nil {
		return "", false, nil
	}
	remoteCookie, err := os.Readlink(filepath.Join(filepath.Dir(socketPath), "SingletonCookie"))
	if err != nil || profileCookie == "" || profileCookie != remoteCookie {
		return "", false, nil
	}
	info, err := os.Stat(socketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return "", false, nil
	}
	return socketPath, true, nil
}

func chromiumSingletonPayload(executable string, args []string) ([]byte, error) {
	currentDirectory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	parts := append([]string{"START", currentDirectory, executable}, args...)
	var payload bytes.Buffer
	for index, part := range parts {
		if strings.IndexByte(part, 0) >= 0 {
			return nil, errors.New("Chromium argument contains a null byte")
		}
		if index > 0 {
			payload.WriteByte(0)
		}
		payload.WriteString(part)
		if payload.Len() > chromiumSingletonMessageLimit {
			return nil, fmt.Errorf("Chromium launch request exceeds %d bytes", chromiumSingletonMessageLimit)
		}
	}
	return payload.Bytes(), nil
}
