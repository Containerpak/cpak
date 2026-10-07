/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package unixsocket

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLongSocketPath(t *testing.T) {
	for _, network := range []string{"unix", "unixpacket"} {
		t.Run(network, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "socket")
			l, err := Listen(network, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { l.Close() })
			if l.Addr().String() != path {
				t.Fatalf("listener changed its filesystem address: %s", l.Addr())
			}
			c, err := DialTimeout(network, path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			s, err := l.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			c.SetDeadline(time.Now().Add(time.Second))
			s.SetDeadline(time.Now().Add(time.Second))
			if _, err = c.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			if n, err := s.Read(buf); err != nil || n != 4 || string(buf) != "ping" {
				t.Fatalf("socket exchange failed: %q, %v", buf, err)
			}
			if err = l.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("socket survived listener close: %v", err)
			}
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("directory permissions changed: %v, %v", info, err)
			}
		})
	}
}

func TestLongSocketRejectsSymlinkedDirectory(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, strings.Repeat("runtime", 20))
	if err := os.Symlink(base, path); err != nil {
		t.Fatal(err)
	}
	if l, err := Listen("unix", filepath.Join(path, "socket")); err == nil {
		l.Close()
		t.Fatal("symlinked socket directory accepted")
	}
}

func TestDialRejectsNonUnixNetwork(t *testing.T) {
	if c, err := DialTimeout("tcp", "127.0.0.1:1", time.Second); err == nil {
		c.Close()
		t.Fatal("non-Unix network accepted")
	}
}

func TestListenerPreservesSocketWhenUnlinkIsDisabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "socket")
	l, err := Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("retained socket is missing: %v, %v", info, err)
	}
}

func TestAddressFailureClosesDirectoryDescriptors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), strings.Repeat("runtime", 20))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if f, _, err := Address(filepath.Join(dir, strings.Repeat("s", 108))); err == nil {
			f.Close()
			t.Fatal("oversized socket name accepted")
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) != len(before) {
		t.Fatalf("directory descriptors leaked: before %d, after %d, %v", len(before), len(after), err)
	}
}
