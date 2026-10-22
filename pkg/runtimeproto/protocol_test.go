/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package runtimeproto

import (
	"bytes"
	"net"
	"os"
	"reflect"
	"syscall"
	"testing"
)

func TestRequestFrameRoundTrip(t *testing.T) {
	want := Request{
		Args:     []string{"app", "--flag"},
		Env:      []string{"LANG=C"},
		AsRoot:   true,
		Terminal: true,
		Rows:     42,
		Columns:  120,
	}
	payload, err := EncodeRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if err = NewWriter(&stream).Write(FrameRequest, payload); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := Read(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameRequest {
		t.Fatalf("frame kind: got %d, want %d", kind, FrameRequest)
	}
	got, err := DecodeRequest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request: got %+v, want %+v", got, want)
	}
}

func TestExitFrameRoundTrip(t *testing.T) {
	for _, want := range []int{0, 1, 127, -1} {
		got, err := DecodeExit(EncodeExit(want))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("exit code: got %d, want %d", got, want)
		}
	}
}

func TestDecodeRequestRejectsEmptyCommand(t *testing.T) {
	if _, err := DecodeRequest([]byte(`{"args":[]}`)); err == nil {
		t.Fatal("empty command was accepted")
	}
}

func TestTerminalSizeFrameRoundTrip(t *testing.T) {
	rows, columns, err := DecodeSize(EncodeSize(42, 120))
	if err != nil {
		t.Fatal(err)
	}
	if rows != 42 || columns != 120 {
		t.Fatalf("terminal size: got %dx%d, want 42x120", columns, rows)
	}
	if _, _, err = DecodeSize([]byte{1, 2, 3}); err == nil {
		t.Fatal("invalid terminal size frame was accepted")
	}
}

func TestRequestCarriesDescriptorsToTheRuntime(t *testing.T) {
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.FileConn(os.NewFile(uintptr(pair[0]), "client"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := net.FileConn(os.NewFile(uintptr(pair[1]), "server"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	request := Request{Args: []string{"helper"}, Directory: "/app", Descriptors: []int{3}}
	if err := WriteRequest(client.(*net.UnixConn), request, []int{int(writer.Fd())}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	kind, payload, files, err := ReadRequest(server.(*net.UnixConn))
	if err != nil {
		t.Fatal(err)
	}
	if kind != FrameRequest || len(files) != 1 {
		t.Fatalf("received frame %d with %d descriptors", kind, len(files))
	}
	got, err := DecodeRequest(payload)
	if err != nil || !reflect.DeepEqual(got, request) {
		t.Fatalf("received %+v: %v", got, err)
	}
	if _, err := files[0].Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	files[0].Close()
	buffer := make([]byte, 1)
	if _, err := reader.Read(buffer); err != nil || buffer[0] != 'x' {
		t.Fatal("the descriptor received is not the one sent")
	}
}

func TestDescriptorTargetsAreValidated(t *testing.T) {
	for _, payload := range []string{
		`{"args":["x"],"descriptors":[3,3]}`,
		`{"args":["x"],"descriptors":[-1]}`,
		`{"args":["x"],"descriptors":[5000]}`,
	} {
		if _, err := DecodeRequest([]byte(payload)); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
	if err := WriteRequest(nil, Request{Args: []string{"x"}, Descriptors: []int{1}}, nil); err == nil {
		t.Fatal("a request naming a descriptor it does not send was written")
	}
}
