/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package grantproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"golang.org/x/sys/unix"
)

const maxMessageSize = 64 << 10

type Request struct {
	Grant filegrant.Grant `json:"grant"`
}

type Response struct {
	Target string `json:"target,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Sources struct {
	Selected *os.File
	Mount    *os.File
}

func (s Sources) Close() {
	if s.Selected != nil {
		_ = s.Selected.Close()
	}
	if s.Mount != nil {
		_ = s.Mount.Close()
	}
}

func Send(socketPath string, grant filegrant.Grant, source, mountSource *os.File) (string, error) {
	if err := grant.Validate(); err != nil {
		return "", err
	}
	if source == nil || grant.Kind == filegrant.KindFile && mountSource == nil {
		return "", errors.New("file grant source descriptor is required")
	}
	connection, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: socketPath, Net: "unixpacket"})
	if err != nil {
		return "", fmt.Errorf("connect to file grant service: %w", err)
	}
	defer connection.Close()
	payload, err := json.Marshal(Request{Grant: grant})
	if err != nil {
		return "", err
	}
	if len(payload) > maxMessageSize {
		return "", errors.New("file grant request is too large")
	}
	descriptors := []int{int(source.Fd())}
	if mountSource != nil {
		descriptors = append(descriptors, int(mountSource.Fd()))
	}
	if _, _, err = connection.WriteMsgUnix(payload, unix.UnixRights(descriptors...), nil); err != nil {
		return "", fmt.Errorf("send file grant request: %w", err)
	}
	buffer := make([]byte, maxMessageSize)
	read, err := connection.Read(buffer)
	if err != nil {
		return "", fmt.Errorf("read file grant response: %w", err)
	}
	var response Response
	decoder := json.NewDecoder(bytes.NewReader(buffer[:read]))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return "", fmt.Errorf("decode file grant response: %w", err)
	}
	if response.Error != "" {
		return "", errors.New(response.Error)
	}
	if response.Target != grant.Target {
		return "", errors.New("file grant service returned an invalid target")
	}
	return response.Target, nil
}

func Receive(connection *net.UnixConn) (Request, Sources, error) {
	buffer := make([]byte, maxMessageSize)
	oob := make([]byte, unix.CmsgSpace(8))
	read, oobRead, flags, _, err := connection.ReadMsgUnix(buffer, oob)
	if err != nil {
		return Request{}, Sources{}, err
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return Request{}, Sources{}, errors.New("file grant request is truncated")
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(buffer[:read]))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return Request{}, Sources{}, fmt.Errorf("decode file grant request: %w", err)
	}
	if err = request.Grant.Validate(); err != nil {
		return Request{}, Sources{}, err
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobRead])
	if err != nil {
		return Request{}, Sources{}, fmt.Errorf("parse file grant descriptor: %w", err)
	}
	fds := []int{}
	for _, message := range messages {
		rights, rightsErr := unix.ParseUnixRights(&message)
		if rightsErr != nil {
			closeDescriptors(fds)
			return Request{}, Sources{}, fmt.Errorf("parse file grant rights: %w", rightsErr)
		}
		fds = append(fds, rights...)
	}
	expected := 1
	if request.Grant.Kind == filegrant.KindFile {
		expected = 2
	}
	if len(fds) != expected {
		closeDescriptors(fds)
		return Request{}, Sources{}, errors.New("file grant request contains an invalid descriptor count")
	}
	sources := Sources{Selected: os.NewFile(uintptr(fds[0]), request.Grant.Source)}
	if expected == 2 {
		sources.Mount = os.NewFile(uintptr(fds[1]), filepath.Dir(request.Grant.Source))
	}
	if sources.Selected == nil || expected == 2 && sources.Mount == nil {
		sources.Close()
		return Request{}, Sources{}, errors.New("open file grant descriptors")
	}
	return request, sources, nil
}

func Reply(connection *net.UnixConn, response Response) error {
	return json.NewEncoder(connection).Encode(response)
}

func closeDescriptors(fds []int) {
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
}
