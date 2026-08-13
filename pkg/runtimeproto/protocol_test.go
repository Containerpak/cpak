/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package runtimeproto

import (
	"bytes"
	"reflect"
	"testing"
)

func TestRequestFrameRoundTrip(t *testing.T) {
	want := Request{Args: []string{"app", "--flag"}, Env: []string{"LANG=C"}, AsRoot: true}
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
