/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mirkobrombin/cpak/pkg/unixsocket"
	"golang.org/x/sys/unix"
)

type xwaylandDisplay struct {
	client   *net.UnixConn
	host     *net.UnixConn
	mu       sync.Mutex
	workers  sync.WaitGroup
	stopOnce sync.Once
	objects  map[uint32]string
	surfaces map[uint32]uint32
	root     uint32
	buffer   bool
	pending  bool
	visible  bool
}

func startXwaylandDisplay() (*xwaylandDisplay, *os.File, error) {
	host, err := unixsocket.DialTimeout("unix", waylandSocketPath(strconv.Itoa(os.Getuid())), time.Second)
	if err != nil {
		return nil, nil, err
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		host.Close()
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fds[0]), "xwayland-display-proxy")
	peer := os.NewFile(uintptr(fds[1]), "xwayland-display")
	connection, err := net.FileConn(file)
	file.Close()
	if err != nil {
		host.Close()
		peer.Close()
		return nil, nil, err
	}
	d := &xwaylandDisplay{
		client: connection.(*net.UnixConn), host: host,
		objects: map[uint32]string{1: "wl_display"}, surfaces: make(map[uint32]uint32),
	}
	d.workers.Add(2)
	go func() {
		defer d.workers.Done()
		defer d.stop()
		_ = d.forwardRequests()
	}()
	go func() {
		defer d.workers.Done()
		defer d.stop()
		_ = copyX11Messages(d.client, d.host)
	}()
	return d, peer, nil
}

func (d *xwaylandDisplay) stop() {
	d.stopOnce.Do(func() {
		_ = d.client.Close()
		_ = d.host.Close()
	})
}

func (d *xwaylandDisplay) close() {
	d.stop()
	d.workers.Wait()
}

func (d *xwaylandDisplay) show() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.visible {
		return nil
	}
	d.visible = true
	if !d.pending {
		return nil
	}
	d.pending = false
	return writeWaylandMessage(d.host, waylandCommit(d.root), nil)
}

func (d *xwaylandDisplay) forwardRequests() error {
	var pendingFDs []int
	defer func() { closeWaylandFDs(pendingFDs) }()
	for {
		message, fds, err := readWaylandMessage(d.client)
		pendingFDs = append(pendingFDs, fds...)
		if err != nil {
			return err
		}
		d.mu.Lock()
		forward := d.filter(message)
		if forward {
			err = writeWaylandMessage(d.host, message, pendingFDs)
			closeWaylandFDs(pendingFDs)
			pendingFDs = nil
		}
		d.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (d *xwaylandDisplay) filter(message []byte) bool {
	id := binary.NativeEndian.Uint32(message)
	opcode := binary.NativeEndian.Uint32(message[4:]) & 0xffff
	args := message[8:]
	switch d.objects[id] {
	case "wl_display":
		if opcode == 1 && len(args) == 4 {
			d.objects[binary.NativeEndian.Uint32(args)] = "wl_registry"
		}
	case "wl_registry":
		if opcode == 0 && len(args) >= 16 {
			length := int(binary.NativeEndian.Uint32(args[4:]))
			if length > 0 && length <= len(args)-16 {
				name := strings.TrimRight(string(args[8:8+length]), "\x00")
				if name == "wl_compositor" || name == "xdg_wm_base" {
					d.objects[binary.NativeEndian.Uint32(args[len(args)-4:])] = name
				}
			}
		}
	case "wl_compositor":
		if opcode == 0 && len(args) == 4 {
			d.objects[binary.NativeEndian.Uint32(args)] = "wl_surface"
		}
	case "xdg_wm_base":
		if opcode == 2 && len(args) == 8 {
			surface := binary.NativeEndian.Uint32(args)
			d.objects[surface] = "xdg_surface"
			d.surfaces[surface] = binary.NativeEndian.Uint32(args[4:])
		}
	case "xdg_surface":
		if opcode == 1 && len(args) == 4 {
			d.root = d.surfaces[id]
		}
	case "wl_surface":
		if id != d.root {
			return true
		}
		if opcode == 1 && len(args) == 12 {
			d.buffer = binary.NativeEndian.Uint32(args) != 0
		}
		// Keep the initial empty commit for xdg configure; hold buffers until a window maps.
		if opcode == 6 && d.buffer && !d.visible {
			d.pending = true
			return false
		}
	}
	return true
}

func waylandCommit(surface uint32) []byte {
	message := make([]byte, 8)
	binary.NativeEndian.PutUint32(message, surface)
	binary.NativeEndian.PutUint32(message[4:], 8<<16|6)
	return message
}

func readWaylandMessage(connection *net.UnixConn) ([]byte, []int, error) {
	message := make([]byte, 8)
	var fds []int
	read := func(data []byte) error {
		control := make([]byte, unix.CmsgSpace(253*4))
		for len(data) > 0 {
			n, oobn, flags, _, err := connection.ReadMsgUnix(data, control)
			messages, controlErr := syscall.ParseSocketControlMessage(control[:oobn])
			for _, message := range messages {
				rights, rightsErr := syscall.ParseUnixRights(&message)
				fds = append(fds, rights...)
				if rightsErr != nil {
					controlErr = rightsErr
				}
			}
			if controlErr != nil {
				return controlErr
			}
			if flags&unix.MSG_CTRUNC != 0 {
				return errors.New("truncated Wayland file descriptors")
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.EOF
			}
			data = data[n:]
		}
		return nil
	}
	if err := read(message); err != nil {
		return nil, fds, err
	}
	size := int(binary.NativeEndian.Uint32(message[4:]) >> 16)
	if size < 8 || size%4 != 0 {
		return nil, fds, errors.New("invalid Wayland message size")
	}
	message = append(message, make([]byte, size-8)...)
	err := read(message[8:])
	return message, fds, err
}

func writeWaylandMessage(connection *net.UnixConn, message []byte, fds []int) error {
	var control []byte
	if len(fds) > 0 {
		control = syscall.UnixRights(fds...)
	}
	written, _, err := connection.WriteMsgUnix(message, control, nil)
	if err != nil {
		return err
	}
	for written < len(message) {
		n, err := connection.Write(message[written:])
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		written += n
	}
	return nil
}

func closeWaylandFDs(fds []int) {
	for _, fd := range fds {
		_ = syscall.Close(fd)
	}
}
