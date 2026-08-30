/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestMain(m *testing.M) {
	if argsFile := os.Getenv("CPAK_NESTED_TEST_ARGS_FILE"); argsFile != "" {
		content := strings.Join(os.Args[1:], "\n") + "\n"
		if err := os.WriteFile(argsFile, []byte(content), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		fmt.Println("nested-output")
		status, err := strconv.Atoi(os.Getenv("CPAK_NESTED_TEST_STATUS"))
		if err != nil {
			os.Exit(125)
		}
		os.Exit(status)
	}
	os.Exit(m.Run())
}

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

func TestHostServiceSocketPathCanBeIsolated(t *testing.T) {
	directory, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	expected := filepath.Join(directory, "cpak-test.sock")
	t.Setenv("CPAK_SERVICE_SOCKET", expected)

	actual, err := HostServiceSocketPath()
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if actual != expected {
		t.Fatalf("socket path: got %s, want %s", actual, expected)
	}
}

// The container is told to look for the service at /tmp/cpak.sock and the host
// side of the spawn inherits that variable, so the host resolver has to refuse
// it: answering it would mount whatever another account left at that name in
// place of the service, and leave the service itself unreachable.
func TestHostServiceSocketPathRefusesASharedDirectory(t *testing.T) {
	t.Setenv("CPAK_SERVICE_SOCKET", ContainerServiceSocketPath)

	path, err := HostServiceSocketPath()
	if err == nil {
		t.Fatalf("a socket in a shared directory was accepted: %s", path)
	}
}

// Refusing a directory is not a licence to repair it. The resolver reads a
// variable somebody else set, so a check that created the parent it was handed,
// or chmodded one it found open, would be changing the machine on the strength
// of an environment variable: CPAK_SERVICE_SOCKET=$HOME/cpak.sock would take
// the home directory down to 0700, and a root caller would do the same to /tmp.
func TestHostServiceSocketPathLeavesADirectoryItRefusesAlone(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("CPAK_SERVICE_SOCKET", filepath.Join(directory, "service.sock"))

	path, err := HostServiceSocketPath()
	if err == nil {
		t.Fatalf("a socket in a directory other accounts may read was accepted: %s", path)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("the refusal changed the directory to %s", info.Mode().Perm())
	}

	// And a parent that is not there is answered rather than made.
	absent := filepath.Join(directory, "made", "by", "the", "check")
	t.Setenv("CPAK_SERVICE_SOCKET", filepath.Join(absent, "service.sock"))
	if path, err = HostServiceSocketPath(); err == nil {
		t.Fatalf("a socket under a directory that does not exist was accepted: %s", path)
	}
	if _, err = os.Stat(absent); !os.IsNotExist(err) {
		t.Fatalf("the check created %s: %v", absent, err)
	}
}

// The socket carries the whole nested request and answers with the output and
// the exit code of the run, so it may not sit on a name another account on the
// machine is free to take first.
func TestHostServiceSocketPathLivesInAPrivateDirectory(t *testing.T) {
	runtimeDirectory, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDirectory) })
	if err = os.Chmod(runtimeDirectory, 0700); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("CPAK_SERVICE_SOCKET", "")

	path, err := HostServiceSocketPath()
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if !strings.HasPrefix(path, runtimeDirectory+string(os.PathSeparator)) {
		t.Fatalf("the socket is outside the private runtime directory: %s", path)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("the socket directory is open to other accounts: %s", info.Mode().Perm())
	}
}

// The two ends of the mount have to agree: the container is told to dial the
// address the spawn command binds the host socket onto, and the host socket is
// the one the service listens on.
func TestSpawnArgumentsPairTheHostSocketWithTheContainerAddress(t *testing.T) {
	runtimeDirectory, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDirectory) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("CPAK_SERVICE_SOCKET", "")

	arguments, err := serviceSocketArguments()
	if err != nil {
		t.Fatalf("service socket arguments: %v", err)
	}
	expected, err := HostServiceSocketPath()
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	want := []string{"--service-socket", expected, "--env", "CPAK_SERVICE_SOCKET=" + ContainerServiceSocketPath}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("spawn arguments: got %v, want %v", arguments, want)
	}
}

func TestSystemBrokerSocketDoesNotReuseTheLegacyProtocol(t *testing.T) {
	runtimeDirectory := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)

	path, err := sharedSystemBrokerSocketPath()
	if err != nil {
		t.Fatalf("system broker socket path: %v", err)
	}
	if filepath.Base(path) != systemBrokerSocketName || filepath.Base(path) == "system-broker.sock" {
		t.Fatalf("system broker socket can reuse an incompatible service: %s", path)
	}
}

func TestNestedServiceArgumentsAreLimitedToDeclaredDependencies(t *testing.T) {
	runtimeDirectory, err := os.MkdirTemp("", "cpak")
	if err != nil {
		t.Fatalf("temporary directory: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDirectory) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeDirectory)
	t.Setenv("CPAK_SERVICE_SOCKET", "")

	plain := types.Application{}
	arguments, err := nestedServiceArguments(plain, "")
	if err != nil || len(arguments) != 0 {
		t.Fatalf("plain application arguments: %v, %v", arguments, err)
	}

	nested := types.Application{ParsedDependencies: []types.Dependency{{Origin: "github.com/example/tool", Mode: "nested"}}}
	if _, err = nestedServiceArguments(nested, ""); err == nil {
		t.Fatal("nested dependencies were accepted without a capability")
	}
	token, err := newNestedToken()
	if err != nil {
		t.Fatal(err)
	}
	arguments, err = nestedServiceArguments(nested, token)
	if err != nil {
		t.Fatal(err)
	}
	if len(arguments) != 6 || arguments[0] != "--nested-token" || arguments[1] != token || arguments[2] != "--service-socket" || arguments[4] != "--env" {
		t.Fatalf("nested service arguments: %v", arguments)
	}
}

// Inside a container the variable is the address of a mount the host made, and
// the default is the target the spawn command binds the host socket onto.
func TestNestedServiceSocketPathIsTheMountTarget(t *testing.T) {
	t.Setenv("CPAK_SERVICE_SOCKET", "")
	if actual := nestedServiceSocketPath(); actual != ContainerServiceSocketPath {
		t.Fatalf("nested socket path: got %s, want %s", actual, ContainerServiceSocketPath)
	}
	t.Setenv("CPAK_SERVICE_SOCKET", "/run/cpak/service.sock")
	if actual := nestedServiceSocketPath(); actual != "/run/cpak/service.sock" {
		t.Fatalf("nested socket path: got %s", actual)
	}
}

func TestBuildNestedRunArgsEncodesTheWholeRequest(t *testing.T) {
	params := types.RequestParams{
		Action:    "run",
		Token:     strings.Repeat("ab", 32),
		Origin:    "github.com/example/app",
		Version:   "1.0.0",
		Branch:    "main",
		Binary:    "app",
		ExtraArgs: []string{"-i", "--version"},
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

// A regular file where the socket belongs is somebody else's doing, and
// removing it silently would hand the next listener a name it did not create.
func TestClearStaleSocketRefusesWhatIsNotASocket(t *testing.T) {
	path := tempSocketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := clearStaleSocket(path); err == nil {
		t.Fatal("a path that is not a socket was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a foreign file was removed: %v", err)
	}
}

// Waiting has to end at once on a name that is not our socket: no listener of
// ours is coming, and the timeout would only delay the report.
func TestWaitForSocketRefusesWhatIsNotASocket(t *testing.T) {
	path := tempSocketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	started := time.Now()
	if err := waitForSocket(path, 10*time.Second); err == nil {
		t.Fatal("a path that is not a socket was accepted")
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("the wait ran to its timeout instead of refusing the path: %s", elapsed)
	}
}

// A socket somebody else owns is never the service of this user, however
// readily it answers, and it is never cleared away either: removing it would be
// this user deleting a file of another account.
func TestValidateSocketOwnerRefusesAForeignSocket(t *testing.T) {
	path := foreignSocket(t)

	if err := validateSocketOwner(path); err == nil {
		t.Fatalf("the socket %s passed for ours", path)
	}
	if _, err := socketIsReady(path); err == nil {
		t.Fatal("a socket of another account was reported as the service")
	}
	if err := clearStaleSocket(path); err == nil {
		t.Fatal("the socket of another account was cleared away")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the socket of another account was removed: %v", err)
	}
}

// foreignSocket answers a socket on this host that belongs to another account.
// An unprivileged process cannot create a file it does not own, so the only
// ones available are the ones the system already runs.
func foreignSocket(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{
		"/run/systemd/journal/socket",
		"/run/systemd/journal/stdout",
		"/run/udev/control",
		"/run/dbus/system_bus_socket",
	} {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSocket == 0 {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
			return candidate
		}
	}
	t.Skip("no socket owned by another account is available on this host")
	return ""
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

func configureNestedTestProcess(t *testing.T, argsFile string, status int) {
	t.Helper()
	t.Setenv("CPAK_NESTED_TEST_ARGS_FILE", argsFile)
	t.Setenv("CPAK_NESTED_TEST_STATUS", strconv.Itoa(status))
}

func TestNestedRunPropagatesArgumentsAndExitStatus(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "argv")
	configureNestedTestProcess(t, argsFile, 7)

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
		Action:    "run",
		Token:     strings.Repeat("ab", 32),
		Origin:    "github.com/example/app",
		Branch:    "main",
		Binary:    "app",
		ExtraArgs: []string{"-i", "--version", "--", "-rf"},
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
	configureNestedTestProcess(t, argsFile, 0)

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

	request, err := json.Marshal(types.RequestParams{Action: "run", Token: strings.Repeat("ab", 32), Origin: "github.com/example/app", Binary: "app"})
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
	configureNestedTestProcess(t, argsFile, 0)

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

	params := types.RequestParams{Action: "run", Token: strings.Repeat("ab", 32), Origin: "github.com/example/app", Binary: "app"}
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
