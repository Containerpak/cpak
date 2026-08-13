/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// tempSocketPath returns a short socket path: a unix socket does not fit in the
// directory names the testing package generates.
func tempSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "cpak.sock")
}

func TestCpakSocketPathCanBeIsolated(t *testing.T) {
	t.Setenv("CPAK_SERVICE_SOCKET", "/tmp/cpak-test.sock")
	if actual := cpakSocketPath(); actual != "/tmp/cpak-test.sock" {
		t.Fatalf("socket path: got %s", actual)
	}
}

func TestBuildNestedRunArgsEncodesTheWholeRequest(t *testing.T) {
	params := types.RequestParams{
		Action:      "run",
		ParentAppId: "parent",
		Origin:      "github.com/example/app",
		Version:     "1.0.0",
		Branch:      "main",
		Binary:      "app",
		ExtraArgs:   []string{"-i", "--version"},
	}

	args, err := BuildNestedRunArgs(params)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(args) != 3 || args[0] != "run" || args[1] != "--nested-request" {
		t.Fatalf("argv: got %v", args)
	}

	// the argv the CLI parser sees carries no end of flags marker and no
	// --version, both of which it used to choke on or hijack
	for _, arg := range args {
		if arg == "--" || arg == "--version" || arg == "-i" {
			t.Fatalf("argv leaks an argument of the application: %v", args)
		}
	}

	got, err := DecodeNestedRequest(args[2])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, params) {
		t.Fatalf("request: got %+v, want %+v", got, params)
	}
}

func TestBuildNestedRunArgsRejectsAnInvalidRequest(t *testing.T) {
	if _, err := BuildNestedRunArgs(types.RequestParams{Action: "run", Binary: "app"}); err == nil {
		t.Fatal("a request without an origin produced an argv")
	}
}

func TestExitCodeFromError(t *testing.T) {
	if code, err := exitCodeFromError(nil); err != nil || code != 0 {
		t.Fatalf("no error: got %d, %v", code, err)
	}

	for _, want := range []int{1, 2, 7, 42} {
		err := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", want)).Run()
		code, codeErr := exitCodeFromError(err)
		if codeErr != nil {
			t.Fatalf("status %d: %v", want, codeErr)
		}
		if code != want {
			t.Fatalf("status: got %d, want %d", code, want)
		}
	}

	// a process killed by a signal reports the status a shell would
	err := exec.Command("/bin/sh", "-c", "kill -TERM $$").Run()
	code, codeErr := exitCodeFromError(err)
	if codeErr != nil {
		t.Fatalf("signalled: %v", codeErr)
	}
	if code != 128+15 {
		t.Fatalf("signalled: got %d, want %d", code, 128+15)
	}

	// anything that is not the status of a process stays an error
	notStarted := errors.New("the command never started")
	if _, err := exitCodeFromError(notStarted); !errors.Is(err, notStarted) {
		t.Fatalf("got %v, want %v", err, notStarted)
	}
}

func TestWaitForSocketGivesUp(t *testing.T) {
	start := time.Now()
	err := waitForSocket(tempSocketPath(t), 150*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("waiting for a socket nobody serves reported success")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the wait took %s, it is not bounded", elapsed)
	}
	if !strings.Contains(err.Error(), "no cpak service answered") {
		t.Fatalf("the error does not say what went wrong: %v", err)
	}
}

func TestWaitForSocketReturnsWhenTheServiceAnswers(t *testing.T) {
	path := tempSocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	if err := waitForSocket(path, time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

// staleSocket leaves a socket file behind with nothing listening on it, the way
// a service killed with SIGKILL does.
func staleSocket(t *testing.T, path string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	if err = syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		syscall.Close(fd)
		t.Fatalf("bind: %v", err)
	}
	syscall.Close(fd)
}

func TestClearStaleSocketRemovesADeadSocket(t *testing.T) {
	path := tempSocketPath(t)
	staleSocket(t, path)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the socket file is already gone: %v", err)
	}
	if err := clearStaleSocket(path); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the stale socket survived: %v", err)
	}
}

// A service has to be able to take over a socket its predecessor left behind.
func TestServeSocketTakesOverAStaleSocket(t *testing.T) {
	path := tempSocketPath(t)
	staleSocket(t, path)

	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service did not take over the stale socket: %v", err)
	}
}

func TestClearStaleSocketKeepsALiveSocket(t *testing.T) {
	path := tempSocketPath(t)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	if err = clearStaleSocket(path); !errors.Is(err, errSocketInUse) {
		t.Fatalf("got %v, want %v", err, errSocketInUse)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("a live socket was removed: %v", err)
	}
}

func TestServeSocketIsIdempotent(t *testing.T) {
	path := tempSocketPath(t)
	cpak := &Cpak{}

	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the first service never came up: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cpak.serveSocket(path) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a second service reported an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second service took over the socket instead of standing down")
	}

	if !socketIsLive(path) {
		t.Fatal("the running service lost its socket")
	}
}

func TestServeSocketRestrictsTheSocketToItsOwner(t *testing.T) {
	path := tempSocketPath(t)
	cpak := &Cpak{}

	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("socket mode: got %o, want %o", perm, 0600)
	}
}

func TestServeSocketStopsWithItsContext(t *testing.T) {
	path := tempSocketPath(t)
	cpak := &Cpak{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cpak.serveSocketContext(ctx, path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("socket listener did not stop")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remains after shutdown: %v", err)
	}
}

// fakeCpak writes a stand in for the cpak binary that records the argv it was
// given, prints a line and exits with the given status.
func fakeCpak(t *testing.T, argsFile string, status int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cpak")
	script := fmt.Sprintf("#!/bin/sh\n: > %[1]s\nfor arg in \"$@\"; do printf '%%s\\n' \"$arg\" >> %[1]s; done\necho nested-output\nexit %[2]d\n", argsFile, status)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write the fake cpak: %v", err)
	}
	return path
}

func TestNestedRunPropagatesArgumentsAndExitStatus(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "argv")
	fake := fakeCpak(t, argsFile, 7)

	// getCpakBinary re-executes the binary of the process, point it at the
	// stand in for the duration of the test
	original := os.Args[0]
	os.Args[0] = fake
	t.Cleanup(func() { os.Args[0] = original })

	path := tempSocketPath(t)
	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	params := types.RequestParams{
		Action:      "run",
		ParentAppId: "parent",
		Origin:      "github.com/example/app",
		Branch:      "main",
		Binary:      "app",
		ExtraArgs:   []string{"-i", "--version", "--", "-rf"},
	}
	request, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = newFrameWriter(conn).write(frameRequest, request); err != nil {
		t.Fatalf("send the request: %v", err)
	}

	var out bytes.Buffer
	err = readNestedResponse(conn, &out)

	var exitErr *types.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("the nested run did not report a status: %v", err)
	}
	if exitErr.Code != 7 {
		t.Fatalf("status: got %d, want 7", exitErr.Code)
	}
	if !strings.Contains(out.String(), "nested-output") {
		t.Fatalf("the output of the nested run is missing: %q", out.String())
	}
	if strings.Contains(out.String(), "OK") {
		t.Fatalf("the output carries an in band status: %q", out.String())
	}

	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read the recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	if len(argv) != 3 || argv[0] != "run" || argv[1] != "--nested-request" {
		t.Fatalf("the nested cpak was called with %v", argv)
	}

	got, err := DecodeNestedRequest(argv[2])
	if err != nil {
		t.Fatalf("the nested cpak received an undecodable request: %v", err)
	}
	if !reflect.DeepEqual(got, params) {
		t.Fatalf("request: got %+v, want %+v", got, params)
	}
}

func TestNestedRunReportsSuccessWithoutAnError(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "argv")
	fake := fakeCpak(t, argsFile, 0)

	original := os.Args[0]
	os.Args[0] = fake
	t.Cleanup(func() { os.Args[0] = original })

	path := tempSocketPath(t)
	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	request, err := json.Marshal(types.RequestParams{Action: "run", ParentAppId: "parent", Origin: "github.com/example/app", Binary: "app"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = newFrameWriter(conn).write(frameRequest, request); err != nil {
		t.Fatalf("send the request: %v", err)
	}

	var out bytes.Buffer
	if err = readNestedResponse(conn, &out); err != nil {
		t.Fatalf("a successful nested run reported %v", err)
	}
	if !strings.Contains(out.String(), "nested-output") {
		t.Fatalf("the output of the nested run is missing: %q", out.String())
	}
}

// The request used to be read with a single 2048 byte Read, so a longer one was
// silently truncated into invalid JSON.
func TestNestedRunAcceptsARequestLongerThanTheOldBuffer(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "argv")
	fake := fakeCpak(t, argsFile, 0)

	original := os.Args[0]
	os.Args[0] = fake
	t.Cleanup(func() { os.Args[0] = original })

	path := tempSocketPath(t)
	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	params := types.RequestParams{Action: "run", ParentAppId: "parent", Origin: "github.com/example/app", Binary: "app"}
	for i := 0; i < 200; i++ {
		params.ExtraArgs = append(params.ExtraArgs, fmt.Sprintf("--argument-number-%d=%s", i, strings.Repeat("v", 32)))
	}
	request, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(request) <= 2048 {
		t.Fatalf("the request is only %d bytes, it does not exercise the old limit", len(request))
	}
	if err = newFrameWriter(conn).write(frameRequest, request); err != nil {
		t.Fatalf("send the request: %v", err)
	}

	var out bytes.Buffer
	if err = readNestedResponse(conn, &out); err != nil {
		t.Fatalf("a long request was rejected: %v", err)
	}

	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read the recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	got, err := DecodeNestedRequest(argv[len(argv)-1])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, params) {
		t.Fatal("the long request did not survive the round trip")
	}
}

func TestNestedRunRejectsAnUnknownAction(t *testing.T) {
	path := tempSocketPath(t)
	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	request, err := json.Marshal(types.RequestParams{Action: "sudo", Origin: "github.com/example/app", Binary: "app"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = newFrameWriter(conn).write(frameRequest, request); err != nil {
		t.Fatalf("send the request: %v", err)
	}

	kind, payload, err := readFrame(conn)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if kind != frameError {
		t.Fatalf("the service answered with a frame of type %d", kind)
	}
	if !strings.Contains(string(payload), "unknown request") {
		t.Fatalf("the answer does not say what went wrong: %q", payload)
	}
}

func TestNestedRunRejectsAFirstFrameThatIsNotARequest(t *testing.T) {
	path := tempSocketPath(t)
	cpak := &Cpak{}
	go func() { _ = cpak.serveSocket(path) }()
	if err := waitForSocket(path, 5*time.Second); err != nil {
		t.Fatalf("the service never came up: %v", err)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err = newFrameWriter(conn).write(frameStdin, []byte("id\n")); err != nil {
		t.Fatalf("send: %v", err)
	}

	kind, _, err := readFrame(conn)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if kind != frameError {
		t.Fatalf("the service answered with a frame of type %d", kind)
	}
}
