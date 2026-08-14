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
	"net"
	"reflect"
	"sync"
	"syscall"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		kind    byte
		payload []byte
	}{
		{"request", frameRequest, []byte(`{"action":"run"}`)},
		{"empty", frameStdinEOF, nil},
		{"binary", frameOutput, []byte{0x00, 0xff, 0x0a, 0x1b, 0x00}},
		{"maximum", frameStdin, bytes.Repeat([]byte{'x'}, maxFramePayload)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := newFrameWriter(&buf).write(tc.kind, tc.payload); err != nil {
				t.Fatalf("write: %v", err)
			}

			kind, payload, err := readFrame(&buf)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if kind != tc.kind {
				t.Fatalf("kind: got %d, want %d", kind, tc.kind)
			}
			if !bytes.Equal(payload, tc.payload) {
				t.Fatalf("payload: got %v, want %v", payload, tc.payload)
			}
			if buf.Len() != 0 {
				t.Fatalf("%d bytes left in the stream", buf.Len())
			}
		})
	}
}

// The protocol this replaced wrote a bare OK into the same stream as the
// application output, so an application printing OK reported success.
func TestFrameCarriesTheOldSentinelAsData(t *testing.T) {
	var buf bytes.Buffer
	if err := newFrameWriter(&buf).write(frameOutput, []byte("OK")); err != nil {
		t.Fatalf("write: %v", err)
	}

	kind, payload, err := readFrame(&buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if kind != frameOutput {
		t.Fatalf("OK was read as a status frame of type %d", kind)
	}
	if string(payload) != "OK" {
		t.Fatalf("payload: got %q, want %q", payload, "OK")
	}
}

func TestReadFrameRejectsTruncatedHeader(t *testing.T) {
	_, _, err := readFrame(bytes.NewReader([]byte{frameOutput, 0x00}))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want an unexpected EOF", err)
	}
}

func TestReadFrameRejectsTruncatedPayload(t *testing.T) {
	frame := []byte{frameOutput, 0x00, 0x00, 0x00, 0x08, 'a', 'b'}
	_, _, err := readFrame(bytes.NewReader(frame))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want an unexpected EOF", err)
	}
}

func TestReadFrameRejectsOversizedLength(t *testing.T) {
	frame := make([]byte, frameHeaderSize)
	frame[0] = frameOutput
	binary.BigEndian.PutUint32(frame[1:], maxFramePayload+1)

	_, _, err := readFrame(bytes.NewReader(frame))
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("got %v, want %v", err, errFrameTooLarge)
	}
}

func TestReadFrameRejectsUnknownType(t *testing.T) {
	for _, kind := range []byte{0, frameError + 1, 0xff} {
		_, _, err := readFrame(bytes.NewReader([]byte{kind, 0, 0, 0, 0}))
		if !errors.Is(err, errUnknownFrame) {
			t.Fatalf("type %d: got %v, want %v", kind, err, errUnknownFrame)
		}
	}
}

func TestWriteFrameRejectsOversizedPayload(t *testing.T) {
	var buf bytes.Buffer
	err := newFrameWriter(&buf).write(frameOutput, make([]byte, maxFramePayload+1))
	if !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("got %v, want %v", err, errFrameTooLarge)
	}
	if buf.Len() != 0 {
		t.Fatalf("a rejected frame wrote %d bytes", buf.Len())
	}
}

func TestFrameWriterSerialisesConcurrentWriters(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	const writers = 8
	const perWriter = 16
	payloads := make([][]byte, writers)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 512)
	}

	read := make(chan error, 1)
	go func() {
		for i := 0; i < writers*perWriter; i++ {
			kind, payload, err := readFrame(server)
			if err != nil {
				read <- err
				return
			}
			if kind != frameOutput {
				read <- errors.New("a frame arrived with the wrong type")
				return
			}
			if !bytes.Equal(payload, bytes.Repeat(payload[:1], len(payload))) {
				read <- errors.New("a frame arrived with mixed payloads")
				return
			}
		}
		read <- nil
	}()

	writer := newFrameWriter(client)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(payload []byte) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if err := writer.write(frameOutput, payload); err != nil {
					return
				}
			}
		}(payloads[i])
	}
	wg.Wait()

	if err := <-read; err != nil {
		t.Fatalf("concurrent writes: %v", err)
	}
}

func TestExitStatusRoundTrip(t *testing.T) {
	for _, code := range []int{0, 1, 2, 7, 127, 130, 255} {
		got, err := decodeExitStatus(encodeExitStatus(code))
		if err != nil {
			t.Fatalf("status %d: %v", code, err)
		}
		if got != code {
			t.Fatalf("status: got %d, want %d", got, code)
		}
	}
}

func TestDecodeExitStatusRejectsMalformedPayload(t *testing.T) {
	for _, payload := range [][]byte{nil, {0x00}, {0x00, 0x00, 0x00, 0x00, 0x00}} {
		if _, err := decodeExitStatus(payload); !errors.Is(err, errMalformedFrame) {
			t.Fatalf("payload %v: got %v, want %v", payload, err, errMalformedFrame)
		}
	}
}

func TestDecodeSignalAcceptsOnlyForwardableSignals(t *testing.T) {
	for sig := range nestedSignals {
		got, err := decodeSignal(encodeSignal(sig))
		if err != nil {
			t.Fatalf("signal %d: %v", sig, err)
		}
		if got != sig {
			t.Fatalf("signal: got %d, want %d", got, sig)
		}
	}

	if _, err := decodeSignal(encodeSignal(syscall.SIGKILL)); err == nil {
		t.Fatal("SIGKILL was accepted as a forwardable signal")
	}
	if _, err := decodeSignal([]byte{0x00}); !errors.Is(err, errMalformedFrame) {
		t.Fatal("a malformed signal payload was accepted")
	}
}

// The arguments of the application are not cpak's to parse: they have to
// survive the round trip untouched, dashes included.
func TestNestedRequestRoundTripKeepsArbitraryArguments(t *testing.T) {
	params := types.RequestParams{
		Action:      "run",
		ParentAppId: "parent",
		Origin:      "github.com/example/app",
		Version:     "1.0.0",
		Branch:      "main",
		Commit:      "0123456789",
		Release:     "v1",
		Binary:      "app",
		ExtraArgs:   []string{"-i", "--version", "--branch", "-", "--", "-rf", "a b", "--nested-request", "ünïcødé"},
	}

	encoded, err := EncodeNestedRequest(params)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, char := range []string{"-", " "} {
		if bytes.Contains([]byte(encoded), []byte(char)) {
			t.Fatalf("the encoded request contains %q, which a CLI parser could read as a flag", char)
		}
	}

	got, err := DecodeNestedRequest(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, params) {
		t.Fatalf("round trip: got %+v, want %+v", got, params)
	}
}

func TestDecodeNestedRequestRejectsInvalidRequests(t *testing.T) {
	valid := types.RequestParams{Action: "run", ParentAppId: "parent", Origin: "github.com/example/app", Binary: "app"}

	if _, err := DecodeNestedRequest("not base64 !!"); err == nil {
		t.Fatal("an unencodable request was accepted")
	}
	if _, err := DecodeNestedRequest("bm90IGpzb24="); err == nil {
		t.Fatal("a request that is not JSON was accepted")
	}
	for _, params := range []types.RequestParams{
		{Action: "shell", Origin: "github.com/example/app", Binary: "app"},
		{Action: "run", Binary: "app"},
		{Action: "run", Origin: "github.com/example/app"},
	} {
		if _, err := EncodeNestedRequest(params); err == nil {
			t.Fatalf("an invalid request was encoded: %+v", params)
		}
	}

	encoded, err := EncodeNestedRequest(valid)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err = DecodeNestedRequest(encoded); err != nil {
		t.Fatalf("a valid request was rejected: %v", err)
	}
}
