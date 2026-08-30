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
	"sync"
)

const MaxFrameSize = 16 * 1024 * 1024

const (
	FrameRequest byte = iota + 1
	FrameInput
	FrameInputClose
	FrameOutput
	FrameExit
	FrameResize
)

type Request struct {
	Args     []string `json:"args"`
	Env      []string `json:"env"`
	AsRoot   bool     `json:"as_root"`
	Terminal bool     `json:"terminal,omitempty"`
	Rows     uint16   `json:"rows,omitempty"`
	Columns  uint16   `json:"columns,omitempty"`
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
	return request, err
}

func EncodeExit(code int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(code)))
	return payload
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
