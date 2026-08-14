/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Frames carried by the nested execution socket. Every message is
// [1 byte type][4 bytes big endian length][payload], so the exit status of a
// nested run can never be mistaken for the bytes the application printed.
const (
	frameRequest  byte = 1 // container -> host, JSON encoded types.RequestParams
	frameStdin    byte = 2 // container -> host, raw stdin bytes
	frameStdinEOF byte = 3 // container -> host, stdin is over
	frameSignal   byte = 4 // container -> host, signal to deliver to the run
	frameOutput   byte = 5 // host -> container, raw stdout and stderr bytes
	frameExit     byte = 6 // host -> container, exit status of the run
	frameError    byte = 7 // host -> container, the run never started
)

// frameHeaderSize is the type byte plus the big endian payload length.
const frameHeaderSize = 5

// maxFramePayload bounds a single frame: neither side allocates on a length
// the other side just made up.
const maxFramePayload = 1 << 20

var (
	errFrameTooLarge  = errors.New("frame payload exceeds the maximum size")
	errUnknownFrame   = errors.New("unknown frame type")
	errMalformedFrame = errors.New("malformed frame payload")
)

// nestedSignals are the only signals a container is allowed to deliver to the
// run it asked for.
var nestedSignals = map[syscall.Signal]bool{
	syscall.SIGINT:  true,
	syscall.SIGTERM: true,
	syscall.SIGHUP:  true,
	syscall.SIGQUIT: true,
}

// isFrameKind reports whether kind is a frame type this protocol defines.
func isFrameKind(kind byte) bool {
	return kind >= frameRequest && kind <= frameError
}

// frameWriter serialises everyone writing to a connection: on the host the
// output pump and the status frames come from different goroutines.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func newFrameWriter(w io.Writer) *frameWriter {
	return &frameWriter{w: w}
}

// write emits a single frame.
func (f *frameWriter) write(kind byte, payload []byte) error {
	if !isFrameKind(kind) {
		return fmt.Errorf("%w: %d", errUnknownFrame, kind)
	}
	if len(payload) > maxFramePayload {
		return errFrameTooLarge
	}

	frame := make([]byte, frameHeaderSize+len(payload))
	frame[0] = kind
	binary.BigEndian.PutUint32(frame[1:frameHeaderSize], uint32(len(payload)))
	copy(frame[frameHeaderSize:], payload)

	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := f.w.Write(frame)
	return err
}

// readFrame reads a single frame, rejecting unknown types and any length the
// protocol does not allow.
func readFrame(r io.Reader) (kind byte, payload []byte, err error) {
	header := make([]byte, frameHeaderSize)
	if _, err = io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}

	kind = header[0]
	if !isFrameKind(kind) {
		return 0, nil, fmt.Errorf("%w: %d", errUnknownFrame, kind)
	}

	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFramePayload {
		return 0, nil, errFrameTooLarge
	}
	if length == 0 {
		return kind, nil, nil
	}

	payload = make([]byte, length)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return kind, payload, nil
}

// encodeExitStatus encodes an exit status for a frameExit payload.
func encodeExitStatus(code int) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(code)))
	return payload
}

// decodeExitStatus decodes a frameExit payload.
func decodeExitStatus(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("%w: exit status of %d bytes", errMalformedFrame, len(payload))
	}
	return int(int32(binary.BigEndian.Uint32(payload))), nil
}

// encodeSignal encodes a signal number for a frameSignal payload.
func encodeSignal(sig syscall.Signal) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(int32(sig)))
	return payload
}

// decodeSignal decodes a frameSignal payload, accepting only the signals a
// container may forward.
func decodeSignal(payload []byte) (syscall.Signal, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("%w: signal of %d bytes", errMalformedFrame, len(payload))
	}
	sig := syscall.Signal(int32(binary.BigEndian.Uint32(payload)))
	if !nestedSignals[sig] {
		return 0, fmt.Errorf("signal %d is not forwardable", sig)
	}
	return sig, nil
}

// EncodeNestedRequest encodes a nested run request into a single argument.
// The request travels encoded because the arguments of the application are not
// cpak's to parse: an argument such as -i or --version would otherwise be read
// as a cpak flag long before the command is reached.
func EncodeNestedRequest(params types.RequestParams) (string, error) {
	if err := validateNestedRequest(params); err != nil {
		return "", err
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	if len(data) > maxFramePayload {
		return "", errFrameTooLarge
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeNestedRequest is the inverse of EncodeNestedRequest.
func DecodeNestedRequest(encoded string) (params types.RequestParams, err error) {
	if base64.StdEncoding.DecodedLen(len(encoded)) > maxFramePayload {
		return params, errFrameTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return params, fmt.Errorf("invalid nested request encoding: %w", err)
	}
	if err = json.Unmarshal(data, &params); err != nil {
		return params, fmt.Errorf("invalid nested request: %w", err)
	}
	return params, validateNestedRequest(params)
}

// validateNestedRequest rejects a request the host would not be able to serve.
func validateNestedRequest(params types.RequestParams) error {
	if params.Action != "run" {
		return fmt.Errorf("unknown request: %s", params.Action)
	}
	if params.Origin == "" {
		return errors.New("the nested request carries no origin")
	}
	if params.ParentAppId == "" {
		return errors.New("the nested request carries no parent application")
	}
	if params.Binary == "" {
		return errors.New("the nested request carries no binary")
	}
	return nil
}
