/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestXwaylandDisplayHoldsRootBuffersWithoutBlockingCursors(t *testing.T) {
	d := &xwaylandDisplay{root: 10, objects: map[uint32]string{10: "wl_surface", 11: "wl_surface"}}
	if !d.filter(waylandCommit(10)) {
		t.Fatal("the empty configure commit was withheld")
	}
	if !d.filter(testWaylandRequest(10, 1, 30, 0, 0)) || d.filter(waylandCommit(10)) {
		t.Fatal("the root buffer was presented before an application window mapped")
	}
	if !d.filter(testWaylandRequest(11, 1, 31, 0, 0)) || !d.filter(waylandCommit(11)) {
		t.Fatal("the root visibility gate blocked a cursor surface")
	}
	d.visible = true
	if !d.filter(waylandCommit(10)) {
		t.Fatal("a visible application could not present its root buffer")
	}
}

func TestXwaylandDisplayPreservesDescriptorsAcrossAHeldCommit(t *testing.T) {
	client, source := testX11SocketPair(t)
	destination, host := testX11SocketPair(t)
	d := &xwaylandDisplay{client: source, host: destination, root: 10, buffer: true, objects: map[uint32]string{10: "wl_surface"}}
	done := make(chan error, 1)
	stopped := false
	go func() { done <- d.forwardRequests() }()
	t.Cleanup(func() {
		client.Close()
		if stopped {
			return
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("the Wayland forwarder did not stop")
		}
	})
	file, err := os.CreateTemp(t.TempDir(), "wayland-buffer")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err = file.WriteString("shared pixels"); err != nil {
		t.Fatal(err)
	}
	before := countX11TestDescriptors(t, file.Name())
	if _, _, err = client.WriteMsgUnix(waylandCommit(10), syscall.UnixRights(int(file.Fd())), nil); err != nil {
		t.Fatal(err)
	}
	message := testWaylandRequest(12, 0, 100, 200)
	if _, err = client.Write(message[:3]); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Write(message[3:]); err != nil {
		t.Fatal(err)
	}
	got, fds, err := readWaylandMessage(host)
	defer func() { closeWaylandFDs(fds) }()
	if err != nil || !bytes.Equal(got, message) || len(fds) != 1 {
		t.Fatalf("Wayland message or descriptor changed: %x, %v, %v", got, fds, err)
	}
	received := os.NewFile(uintptr(fds[0]), "received-wayland-buffer")
	fds = nil
	data := make([]byte, 13)
	_, err = received.ReadAt(data, 0)
	received.Close()
	if err != nil || string(data) != "shared pixels" {
		t.Fatalf("Wayland descriptor is unusable: %q, %v", data, err)
	}
	client.Close()
	err = <-done
	stopped = true
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Wayland forwarder exit: %v", err)
	}
	if after := countX11TestDescriptors(t, file.Name()); after != before {
		t.Fatalf("Wayland descriptor leaked: before %d, after %d", before, after)
	}
}

func TestXwaylandDisplayShowsAPendingBufferOnce(t *testing.T) {
	destination, host := testX11SocketPair(t)
	d := &xwaylandDisplay{host: destination, root: 10, pending: true}
	if err := d.show(); err != nil {
		t.Fatal(err)
	}
	message, fds, err := readWaylandMessage(host)
	defer closeWaylandFDs(fds)
	if err != nil || !bytes.Equal(message, waylandCommit(10)) || !d.visible || d.pending {
		t.Fatalf("pending Wayland buffer did not map: %x, %v", message, err)
	}
	if err = d.show(); err != nil {
		t.Fatal(err)
	}
	if err = host.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = host.Read(make([]byte, 8)); err == nil {
		t.Fatal("showing an already visible display committed the buffer again")
	}
}

func TestXwaylandMessageRejectsInvalidSize(t *testing.T) {
	for _, size := range []uint32{0, 4, 9} {
		sender, receiver := testX11SocketPair(t)
		message := testWaylandRequest(1, 0)
		binary.NativeEndian.PutUint32(message[4:], size<<16)
		if _, err := sender.Write(message); err != nil {
			t.Fatal(err)
		}
		_, fds, err := readWaylandMessage(receiver)
		closeWaylandFDs(fds)
		if err == nil {
			t.Fatalf("invalid Wayland message size %d was accepted", size)
		}
	}
}

func testWaylandRequest(id, opcode uint32, args ...uint32) []byte {
	message := make([]byte, 8+4*len(args))
	binary.NativeEndian.PutUint32(message, id)
	binary.NativeEndian.PutUint32(message[4:], uint32(len(message))<<16|opcode)
	for i, arg := range args {
		binary.NativeEndian.PutUint32(message[8+4*i:], arg)
	}
	return message
}
