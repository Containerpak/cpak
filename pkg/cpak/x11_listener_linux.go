/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/binary"
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func pendingX11Client(listener *os.File, client *net.UnixConn) (bool, error) {
	var socket, peer unix.Stat_t
	if err := unix.Fstat(int(listener.Fd()), &socket); err != nil {
		return false, err
	}
	control, err := client.SyscallConn()
	if err != nil {
		return false, err
	}
	var statErr error
	if err = control.Control(func(fd uintptr) { statErr = unix.Fstat(int(fd), &peer) }); err != nil {
		return false, err
	}
	if statErr != nil {
		return false, statErr
	}
	if socket.Ino > 1<<32-1 || peer.Ino > 1<<32-1 {
		return false, errors.New("X11 socket identity exceeds the kernel diagnostic format")
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return false, err
	}
	defer unix.Close(fd)
	if err = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1}); err != nil {
		return false, err
	}
	order := binary.NativeEndian
	request := make([]byte, 40)
	order.PutUint32(request, uint32(len(request)))
	order.PutUint16(request[4:], unix.SOCK_DIAG_BY_FAMILY)
	order.PutUint16(request[6:], unix.NLM_F_REQUEST)
	order.PutUint32(request[8:], 1)
	request[16] = unix.AF_UNIX
	order.PutUint32(request[20:], 1<<32-1)
	order.PutUint32(request[24:], uint32(socket.Ino))
	order.PutUint32(request[28:], 8) // UDIAG_SHOW_ICONS: queued clients, not accepted peers.
	order.PutUint32(request[32:], 1<<32-1)
	order.PutUint32(request[36:], 1<<32-1)
	if err = unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return false, err
	}
	response := make([]byte, 4096)
	n, sender, err := unix.Recvfrom(fd, response, 0)
	if err != nil {
		return false, err
	}
	kernel, ok := sender.(*unix.SockaddrNetlink)
	if !ok || kernel.Pid != 0 || n < 32 || order.Uint32(response[8:]) != 1 {
		return false, errors.New("invalid X11 socket diagnostic response")
	}
	size := int(order.Uint32(response))
	if size < 32 || size > n || order.Uint16(response[4:]) != unix.SOCK_DIAG_BY_FAMILY || order.Uint32(response[20:]) != uint32(socket.Ino) {
		return false, errors.New("X11 socket diagnostic does not match the listener")
	}
	for offset := 32; offset+4 <= size; {
		length := int(order.Uint16(response[offset:]))
		if length < 4 || offset+length > size {
			return false, errors.New("invalid X11 socket diagnostic attribute")
		}
		if order.Uint16(response[offset+2:]) == 3 { // UNIX_DIAG_ICONS.
			for index := offset + 4; index+4 <= offset+length; index += 4 {
				if order.Uint32(response[index:]) == uint32(peer.Ino) {
					return true, nil
				}
			}
		}
		offset += (length + 3) &^ 3
	}
	return false, nil
}
