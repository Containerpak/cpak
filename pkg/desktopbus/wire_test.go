/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"encoding/binary"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func TestReadMessageLimitsFileDescriptorsAcrossReads(t *testing.T) {
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	sender := unixConnection(t, sockets[0])
	receiver := unixConnection(t, sockets[1])
	defer sender.Close()
	defer receiver.Close()

	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeReader.Close()
	defer pipeWriter.Close()

	message := &dbus.Message{
		Type: dbus.TypeMethodCall,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/example/Test")),
			dbus.FieldMember:      dbus.MakeVariant("Ping"),
			dbus.FieldDestination: dbus.MakeVariant("org.example.Test"),
		},
	}
	data, _, err := encodeMessage(message, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	rights := make([]int, maxMessageFDs/2+1)
	for index := range rights {
		rights[index] = int(pipeReader.Fd())
	}
	if _, _, err = sender.WriteMsgUnix(data[:16], unix.UnixRights(rights...), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sender.WriteMsgUnix(data[16:], unix.UnixRights(rights...), nil); err != nil {
		t.Fatal(err)
	}
	_, err = readMessage(receiver)
	if err == nil || !strings.Contains(err.Error(), "too many file descriptors") {
		t.Fatalf("got %v, want the per-message file descriptor limit", err)
	}
}

func unixConnection(t *testing.T, fd int) *net.UnixConn {
	t.Helper()
	file := os.NewFile(uintptr(fd), "desktop-bus-test")
	connection, err := net.FileConn(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	return connection.(*net.UnixConn)
}
