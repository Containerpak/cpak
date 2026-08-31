/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const x11AuthorityTarget = "/run/cpak/xauthority"

var findX11Server = exec.LookPath

type x11Server struct {
	command       *exec.Cmd
	privateSocket bool
}

func startX11Bridge(container types.Container) (types.Container, error) {
	authorityPath := filepath.Join(container.StatePath, "xauthority")
	if err := writeX11Authority(authorityPath); err != nil {
		return container, err
	}
	server, err := x11ServerCommand(authorityPath)
	if err != nil {
		_ = os.Remove(authorityPath)
		return container, err
	}
	displayReader, displayWriter, err := os.Pipe()
	if err != nil {
		_ = os.Remove(authorityPath)
		return container, fmt.Errorf("create X11 display pipe: %w", err)
	}
	defer displayReader.Close()
	command := server.command
	socketPath := ""
	var listener *net.UnixListener
	var listenerFile *os.File
	closePrivateListener := func(remove bool) {
		if listenerFile != nil {
			_ = listenerFile.Close()
			listenerFile = nil
		}
		if listener != nil {
			_ = listener.Close()
			listener = nil
		}
		if remove && socketPath != "" {
			_ = os.Remove(socketPath)
		}
	}
	if server.privateSocket {
		socketPath = filepath.Join(container.StatePath, "x11.sock")
		listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
		if err != nil {
			displayWriter.Close()
			_ = os.Remove(authorityPath)
			return container, fmt.Errorf("create private X11 socket: %w", err)
		}
		listener.SetUnlinkOnClose(false)
		if err = os.Chmod(socketPath, 0600); err != nil {
			closePrivateListener(true)
			displayWriter.Close()
			_ = os.Remove(authorityPath)
			return container, fmt.Errorf("restrict private X11 socket: %w", err)
		}
		listenerFile, err = listener.File()
		if err != nil {
			closePrivateListener(true)
			displayWriter.Close()
			_ = os.Remove(authorityPath)
			return container, fmt.Errorf("pass private X11 socket: %w", err)
		}
		command.Args = append(command.Args, "-listenfd", "3", "-displayfd", "4")
		command.ExtraFiles = []*os.File{listenerFile, displayWriter}
	} else {
		command.Args = append(command.Args, "-displayfd", "3")
		command.ExtraFiles = []*os.File{displayWriter}
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logFile, err := os.OpenFile(container.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		closePrivateListener(true)
		displayWriter.Close()
		_ = os.Remove(authorityPath)
		return container, fmt.Errorf("open X11 bridge log: %w", err)
	}
	defer logFile.Close()
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	if err = command.Start(); err != nil {
		closePrivateListener(true)
		displayWriter.Close()
		_ = os.Remove(authorityPath)
		return container, fmt.Errorf("start isolated X11 display: %w", err)
	}
	closePrivateListener(false)
	displayWriter.Close()
	display, err := readX11Display(displayReader, 5*time.Second)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = os.Remove(authorityPath)
		_ = os.Remove(socketPath)
		return container, err
	}
	socketTarget := filepath.Join("/tmp/.X11-unix", "X"+display)
	if socketPath == "" {
		socketPath = socketTarget
	}
	if err = waitForX11Socket(socketPath, 3*time.Second); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = os.Remove(authorityPath)
		if server.privateSocket {
			_ = os.Remove(socketPath)
		}
		return container, fmt.Errorf("start isolated X11 display: %w", err)
	}
	container.X11BridgePid = command.Process.Pid
	container.X11BridgeStartTime, err = processStartTime(command.Process.Pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = os.Remove(authorityPath)
		if server.privateSocket {
			_ = os.Remove(socketPath)
		}
		return container, fmt.Errorf("identify isolated X11 display: %w", err)
	}
	container.X11Display = ":" + display
	container.X11SocketPath = socketPath
	container.X11SocketTarget = socketTarget
	container.X11AuthorityPath = authorityPath
	_ = command.Process.Release()
	return container, nil
}

func x11ServerCommand(authorityPath string) (x11Server, error) {
	uid := strconv.Itoa(os.Getuid())
	if os.Getenv("WAYLAND_DISPLAY") != "" && socketIsLive(waylandSocketPath(uid)) {
		server, err := findX11Server("Xwayland")
		if err == nil {
			return x11Server{command: exec.Command(server, "-auth", authorityPath, "-nolisten", "tcp", "-geometry", "1280x800"), privateSocket: true}, nil
		}
	}
	if os.Getenv("DISPLAY") != "" {
		server, err := findX11Server("Xephyr")
		if err == nil {
			return x11Server{command: exec.Command(server, "-auth", authorityPath, "-nolisten", "tcp", "-screen", "1280x800")}, nil
		}
	}
	return x11Server{}, errors.New("displayX11 requires Xwayland on Wayland or Xephyr on X11")
}

func readX11Display(reader io.Reader, timeout time.Duration) (string, error) {
	result := make(chan string, 1)
	failure := make(chan error, 1)
	go func() {
		value, err := bufio.NewReader(reader).ReadString('\n')
		if err != nil {
			failure <- err
			return
		}
		result <- strings.TrimSpace(value)
	}()
	select {
	case display := <-result:
		value, err := strconv.Atoi(display)
		if err != nil || value < 0 || value > 1023 {
			return "", errors.New("X11 server returned an invalid display number")
		}
		return display, nil
	case err := <-failure:
		return "", fmt.Errorf("read X11 display number: %w", err)
	case <-time.After(timeout):
		return "", errors.New("X11 server did not become ready")
	}
}

func writeX11Authority(path string) error {
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return fmt.Errorf("generate X11 authority: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create X11 authority: %w", err)
	}
	defer file.Close()
	hostname, err := os.Hostname()
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("read hostname for X11 authority: %w", err)
	}
	for display := 0; display <= 1023; display++ {
		for _, record := range []struct {
			family  uint16
			address string
		}{{256, hostname}, {0xffff, ""}} {
			if err = writeX11AuthorityRecord(file, record.family, record.address, strconv.Itoa(display), cookie); err != nil {
				_ = os.Remove(path)
				return fmt.Errorf("write X11 authority: %w", err)
			}
		}
	}
	return nil
}

func writeX11AuthorityRecord(writer io.Writer, family uint16, address, display string, cookie []byte) error {
	if err := binary.Write(writer, binary.BigEndian, family); err != nil {
		return err
	}
	for _, value := range [][]byte{[]byte(address), []byte(display), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		if err := binary.Write(writer, binary.BigEndian, uint16(len(value))); err != nil {
			return err
		}
		if _, err := writer.Write(value); err != nil {
			return err
		}
	}
	return nil
}

func cleanupX11Bridge(container types.Container) {
	if sameRecordedProcess(container.X11BridgePid, container.X11BridgeStartTime) {
		_ = syscall.Kill(container.X11BridgePid, syscall.SIGTERM)
	}
	if container.X11AuthorityPath != "" {
		_ = os.Remove(container.X11AuthorityPath)
	}
	if strings.HasPrefix(filepath.Clean(container.X11SocketPath), filepath.Clean(container.StatePath)+string(filepath.Separator)) {
		_ = os.Remove(container.X11SocketPath)
	}
}

func waitForX11Socket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := validateSocketOwner(path)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("isolated X11 display did not create %s within %s", path, timeout)
		}
		time.Sleep(socketWaitInterval)
	}
}

func containerX11BridgeAlive(container types.Container) bool {
	if container.X11SocketPath == "" {
		return true
	}
	return sameRecordedProcess(container.X11BridgePid, container.X11BridgeStartTime) && validateSocketOwner(container.X11SocketPath) == nil
}
