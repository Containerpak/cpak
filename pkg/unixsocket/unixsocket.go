/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package unixsocket

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Address pins the parent directory when path exceeds sockaddr_un's limit.
// Keep the returned file open until the bind or connect has finished.
func Address(path string) (*os.File, string, error) {
	if len(path) < 108 {
		return nil, path, nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", fmt.Errorf("unixsocket: long socket path must be absolute and clean")
	}
	fd, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	f := os.NewFile(uintptr(fd), path)
	address := fmt.Sprintf("/proc/self/fd/%d/%s", fd, filepath.Base(path))
	if len(address) >= 108 {
		f.Close()
		return nil, "", fmt.Errorf("unixsocket: socket name is too long")
	}
	return f, address, nil
}

// Listener removes the original socket path rather than a descriptor alias.
type Listener struct {
	*net.UnixListener
	path   string
	unlink bool
}

func (l *Listener) Close() error {
	err := l.UnixListener.Close()
	if err == nil && l.unlink {
		_ = os.Remove(l.path)
	}
	return err
}

func (l *Listener) SetUnlinkOnClose(unlink bool) {
	l.unlink = unlink
}

func (l *Listener) Addr() net.Addr {
	return &net.UnixAddr{Name: l.path, Net: l.UnixListener.Addr().Network()}
}

// Listen keeps the socket's filesystem path independent of its bind address.
func Listen(network, path string) (*Listener, error) {
	f, address, err := Address(path)
	if err != nil {
		return nil, err
	}
	if f != nil {
		defer f.Close()
	}
	l, err := net.ListenUnix(network, &net.UnixAddr{Name: address, Net: network})
	if err != nil {
		return nil, err
	}
	l.SetUnlinkOnClose(false)
	return &Listener{UnixListener: l, path: path, unlink: true}, nil
}

// DialTimeout resolves a long path only for the duration of connect.
func DialTimeout(network, path string, timeout time.Duration) (*net.UnixConn, error) {
	if network != "unix" && network != "unixpacket" && network != "unixgram" {
		return nil, fmt.Errorf("unixsocket: unsupported network %q", network)
	}
	f, address, err := Address(path)
	if err != nil {
		return nil, err
	}
	if f != nil {
		defer f.Close()
	}
	c, err := net.DialTimeout(network, address, timeout)
	if err != nil {
		return nil, err
	}
	return c.(*net.UnixConn), nil
}
