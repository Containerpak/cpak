/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestX11ProxyPreservesFileDescriptors(t *testing.T) {
	for _, direction := range []string{"client-to-server", "server-to-client"} {
		t.Run(direction, func(t *testing.T) {
			client, clientProxy := testX11SocketPair(t)
			serverProxy, server := testX11SocketPair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				proxyX11Connection(ctx, clientProxy, serverProxy)
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("X11 proxy did not stop after cancellation")
				}
			})
			sender, receiver := client, server
			if direction == "server-to-client" {
				sender, receiver = server, client
			}
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer read.Close()
			defer write.Close()
			if _, err = write.WriteString("shared buffer"); err != nil {
				t.Fatal(err)
			}
			write.Close()
			payload := []byte("X11 reply")
			if _, _, err = sender.WriteMsgUnix(payload, syscall.UnixRights(int(read.Fd())), nil); err != nil {
				t.Fatal(err)
			}
			buffer, ancillary := make([]byte, 32), make([]byte, syscall.CmsgSpace(4))
			n, oobn, _, _, err := receiver.ReadMsgUnix(buffer, ancillary)
			if err != nil {
				t.Fatal(err)
			}
			if string(buffer[:n]) != string(payload) {
				t.Fatalf("X11 payload changed: %q", buffer[:n])
			}
			messages, err := syscall.ParseSocketControlMessage(ancillary[:oobn])
			if err != nil {
				t.Fatal(err)
			}
			if len(messages) != 1 {
				t.Fatalf("X11 descriptor was lost: got %d control messages", len(messages))
			}
			fds, err := syscall.ParseUnixRights(&messages[0])
			if err != nil {
				t.Fatal(err)
			}
			if len(fds) != 1 {
				for _, fd := range fds {
					syscall.Close(fd)
				}
				t.Fatalf("got %d X11 descriptors, want 1", len(fds))
			}
			file := os.NewFile(uintptr(fds[0]), "forwarded-buffer")
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "shared buffer" {
				t.Fatalf("forwarded descriptor is unusable: %q, %v", data, err)
			}
		})
	}
}

func TestX11ProxyClosesReceivedDescriptors(t *testing.T) {
	for _, outcome := range []string{"forwarded", "truncated", "write-failed"} {
		t.Run(outcome, func(t *testing.T) {
			sender, source := testX11SocketPair(t)
			destination, receiver := testX11SocketPair(t)
			file, err := os.CreateTemp(t.TempDir(), "shared-buffer")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			control := make([]byte, syscall.CmsgSpace(3*4))
			if outcome == "truncated" {
				control = make([]byte, syscall.CmsgSpace(4))
			}
			if outcome == "write-failed" {
				receiver.Close()
			}
			before := countX11TestDescriptors(t, file.Name())
			for i := 0; i < 32; i++ {
				rights := syscall.UnixRights(int(file.Fd()), int(file.Fd()), int(file.Fd()))
				if _, _, err = sender.WriteMsgUnix([]byte("reply"), rights, nil); err != nil {
					t.Fatal(err)
				}
				err = forwardX11Message(destination, source, make([]byte, 32), control)
				if outcome != "forwarded" {
					if err == nil {
						t.Fatal("invalid descriptor transfer was accepted")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				_, n, _, _, err := receiver.ReadMsgUnix(make([]byte, 32), control)
				if err != nil {
					t.Fatal(err)
				}
				messages, err := syscall.ParseSocketControlMessage(control[:n])
				if err != nil {
					t.Fatal(err)
				}
				for _, message := range messages {
					fds, err := syscall.ParseUnixRights(&message)
					if err != nil {
						t.Fatal(err)
					}
					for _, fd := range fds {
						syscall.Close(fd)
					}
					if len(fds) != 3 {
						t.Fatalf("got %d descriptors, want 3", len(fds))
					}
				}
			}
			after := countX11TestDescriptors(t, file.Name())
			if before != 1 || after != before {
				t.Fatalf("X11 proxy leaked descriptors: before %d, after %d", before, after)
			}
		})
	}
}

func countX11TestDescriptors(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && target == path {
			count++
		}
	}
	return count
}

func TestX11ProxyPreservesLargePayloadAndStops(t *testing.T) {
	client, clientProxy := testX11SocketPair(t)
	serverProxy, server := testX11SocketPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		proxyX11Connection(ctx, clientProxy, serverProxy)
		close(done)
	}()
	payload := bytes.Repeat([]byte("X11 data"), 16*1024)
	written := make(chan error, 1)
	go func() {
		_, err := client.Write(payload)
		written <- err
	}()
	data := make([]byte, len(payload))
	if _, err := io.ReadFull(server, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("X11 payload changed across proxy buffers")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("X11 proxy did not stop while waiting for input")
	}
}

func TestX11BrokerDisablesDriWithoutDevicePermission(t *testing.T) {
	previous := x11ServerSupportsHiDPI
	x11ServerSupportsHiDPI = func(string) bool { return true }
	defer func() { x11ServerSupportsHiDPI = previous }()
	for _, allowed := range []bool{false, true} {
		arguments := x11BrokerServerArguments(X11BrokerOptions{X11Server: "Xwayland", NestedAuthority: "private", DeviceDri: allowed})
		disabled := slices.Contains(arguments, "DRI3")
		if disabled == allowed {
			t.Fatalf("DRI3 permission %v produced arguments %v", allowed, arguments)
		}
		if !slices.Contains(arguments, "-hidpi") {
			t.Fatal("DRI policy lost the Xwayland scale option")
		}
	}
}

func testX11SocketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]*net.UnixConn, 0, 2)
	for _, fd := range fds {
		file := os.NewFile(uintptr(fd), "x11-proxy-test")
		connection, err := net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		unixConnection := connection.(*net.UnixConn)
		t.Cleanup(func() { unixConnection.Close() })
		if err = unixConnection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		connections = append(connections, unixConnection)
	}
	return connections[0], connections[1]
}
