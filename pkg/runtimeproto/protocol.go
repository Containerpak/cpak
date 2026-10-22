/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package runtimeproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
)

const MaxFrameSize = 16 * 1024 * 1024

const (
	FrameRequest byte = iota + 1
	FrameInput
	FrameInputClose
	FrameOutput
	FrameExit
	FrameResize
	FrameStarted
)

type Request struct {
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	AsRoot   bool     `json:"as_root"`
	Terminal bool     `json:"terminal,omitempty"`
	Rows     uint16   `json:"rows,omitempty"`
	Columns  uint16   `json:"columns,omitempty"`

	// Directory is where the command starts; empty keeps the runtime's own.
	Directory string `json:"directory,omitempty"`

	// Descriptors are the numbers the command receives the descriptors sent
	// with the request at, in the order they were sent. A request that sends
	// descriptors gets no streamed input or output: its standard streams are
	// among them, and only the exit frame comes back.
	Descriptors []int `json:"descriptors,omitempty"`
}

// MaxDescriptors bounds what a request may hand to the command it starts.
const MaxDescriptors = 64

// WriteRequest sends a request frame together with descriptors, which
// travel as one message so the receiver finds them on the frame they belong
// to.
func WriteRequest(connection *net.UnixConn, request Request, descriptors []int) error {
	if len(descriptors) != len(request.Descriptors) || len(descriptors) > MaxDescriptors {
		return fmt.Errorf("runtime request names %d descriptors and sends %d", len(request.Descriptors), len(descriptors))
	}
	payload, err := EncodeRequest(request)
	if err != nil {
		return err
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("runtime frame exceeds %d bytes", MaxFrameSize)
	}
	frame := make([]byte, 5, 5+len(payload))
	frame[0] = FrameRequest
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	frame = append(frame, payload...)
	var rights []byte
	if len(descriptors) > 0 {
		rights = syscall.UnixRights(descriptors...)
	}
	written, _, err := connection.WriteMsgUnix(frame, rights, nil)
	if err != nil {
		return err
	}
	if written != len(frame) {
		_, err = connection.Write(frame[written:])
	}
	return err
}

// ReadRequest reads the first frame of a connection and the descriptors sent
// with it. Descriptors arriving without a request are closed and refused.
func ReadRequest(connection *net.UnixConn) (byte, []byte, []*os.File, error) {
	header := make([]byte, 5)
	oob := make([]byte, syscall.CmsgSpace(MaxDescriptors*4))
	read, oobRead, _, _, err := connection.ReadMsgUnix(header, oob)
	if err != nil {
		return 0, nil, nil, err
	}
	files, err := receivedFiles(oob[:oobRead])
	if err != nil {
		return 0, nil, nil, err
	}
	fail := func(err error) (byte, []byte, []*os.File, error) {
		for _, file := range files {
			file.Close()
		}
		return 0, nil, nil, err
	}
	if _, err := io.ReadFull(connection, header[read:]); err != nil {
		return fail(err)
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxFrameSize {
		return fail(fmt.Errorf("runtime frame exceeds %d bytes", MaxFrameSize))
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(connection, payload); err != nil {
		return fail(err)
	}
	if header[0] != FrameRequest && len(files) > 0 {
		return fail(fmt.Errorf("runtime descriptors arrived without a request"))
	}
	return header[0], payload, files, nil
}

func receivedFiles(oob []byte) ([]*os.File, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("read runtime descriptors: %w", err)
	}
	var files []*os.File
	for _, message := range messages {
		fds, err := syscall.ParseUnixRights(&message)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			syscall.CloseOnExec(fd)
			files = append(files, os.NewFile(uintptr(fd), "runtime-descriptor"))
		}
	}
	return files, nil
}

type Writer struct {
	writer io.Writer
	mu     sync.Mutex
}

func NewWriter(writer io.Writer) *Writer {
	return &Writer{writer: writer}
}

func (w *Writer) Write(kind byte, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("runtime frame exceeds %d bytes", MaxFrameSize)
	}
	header := make([]byte, 5)
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.writer.Write(header); err != nil {
		return err
	}
	_, err := w.writer.Write(payload)
	return err
}

func Read(reader io.Reader) (byte, []byte, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > MaxFrameSize {
		return 0, nil, fmt.Errorf("runtime frame exceeds %d bytes", MaxFrameSize)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func EncodeRequest(request Request) ([]byte, error) {
	return json.Marshal(request)
}

func DecodeRequest(payload []byte) (Request, error) {
	var request Request
	err := json.Unmarshal(payload, &request)
	if err == nil && len(request.Args) == 0 {
		err = fmt.Errorf("runtime command is empty")
	}
	if err == nil && len(request.Descriptors) > MaxDescriptors {
		err = fmt.Errorf("runtime command names too many descriptors")
	}
	seen := map[int]bool{}
	for _, target := range request.Descriptors {
		if err == nil && (target < 0 || target >= 1024 || seen[target]) {
			err = fmt.Errorf("runtime command names an invalid descriptor %d", target)
		}
		seen[target] = true
	}
	return request, err
}

func EncodeExit(code int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(code)))
	return payload
}

// EncodeStarted carries the process id of a command that started, as the
// runtime that started it sees it.
func EncodeStarted(pid int) []byte {
	return EncodeExit(pid)
}

func DecodeStarted(payload []byte) (int, error) {
	pid, err := DecodeExit(payload)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid runtime start frame")
	}
	return pid, nil
}

func DecodeExit(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("invalid runtime exit frame")
	}
	return int(int32(binary.BigEndian.Uint32(payload))), nil
}

func EncodeSize(rows, columns uint16) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload, rows)
	binary.BigEndian.PutUint16(payload[2:], columns)
	return payload
}

func DecodeSize(payload []byte) (uint16, uint16, error) {
	if len(payload) != 4 {
		return 0, 0, fmt.Errorf("invalid terminal size frame")
	}
	return binary.BigEndian.Uint16(payload), binary.BigEndian.Uint16(payload[2:]), nil
}
