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
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestX11AuthorityCoversEveryDisplayWithOneCookie(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xauthority")
	if err := writeX11Authority(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("authority mode: %o", info.Mode().Perm())
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(encoded)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	var firstCookie []byte
	for display := 0; display <= 1023; display++ {
		for record := 0; record < 2; record++ {
			family, fields, readErr := readX11AuthorityRecord(reader)
			if readErr != nil {
				t.Fatalf("read display %d record %d: %v", display, record, readErr)
			}
			wantFamily := uint16(256)
			wantAddress := hostname
			if record == 1 {
				wantFamily = 0xffff
				wantAddress = ""
			}
			if family != wantFamily || string(fields[0]) != wantAddress || string(fields[1]) != strconv.Itoa(display) || string(fields[2]) != "MIT-MAGIC-COOKIE-1" || len(fields[3]) != 16 {
				t.Fatalf("display %d authority: family=%d fields=%q", display, family, fields[:3])
			}
			if display == 0 && record == 0 {
				firstCookie = append([]byte{}, fields[3]...)
			} else if !bytes.Equal(firstCookie, fields[3]) {
				t.Fatalf("display %d has another cookie", display)
			}
		}
	}
	if reader.Len() != 0 {
		t.Fatalf("authority has %d trailing bytes", reader.Len())
	}
}

func TestX11DisplayReaderRejectsInvalidAnswers(t *testing.T) {
	if display, err := readX11Display(bytes.NewBufferString("42\n"), time.Second); err != nil || display != "42" {
		t.Fatalf("display: %q, %v", display, err)
	}
	for _, answer := range []string{"", "-1\n", "1024\n", "host:0\n"} {
		if _, err := readX11Display(bytes.NewBufferString(answer), time.Second); err == nil {
			t.Fatalf("invalid display %q was accepted", answer)
		}
	}
}

func TestX11ServerFallsBackToXephyrOnX11(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	original := findX11Server
	findX11Server = func(name string) (string, error) {
		if name == "Xephyr" {
			return "/usr/bin/Xephyr", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { findX11Server = original })
	server, err := x11ServerCommand("/tmp/authority", "cpak-test", types.ClipboardGrant{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/Xephyr", "-auth", "/tmp/authority", "-nolisten", "tcp", "-screen", "1280x800", "-resizeable", "-name", "cpak-test"}
	if !reflect.DeepEqual(server.command.Args, want) {
		t.Fatalf("Xephyr arguments: got %v, want %v", server.command.Args, want)
	}
}

func TestXwaylandRefusesAnUndisclosedClipboardDirection(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" || !socketIsLive(waylandSocketPath(strconv.Itoa(os.Getuid()))) {
		t.Skip("no Wayland display")
	}
	for _, clipboard := range []types.ClipboardGrant{
		{},
		{HostToApp: true},
		{AppToHost: true},
	} {
		if _, err := x11ServerCommand("/tmp/authority", "cpak-test", clipboard); err == nil {
			t.Fatalf("Xwayland accepted an undisclosed clipboard direction: %+v", clipboard)
		}
	}
}

func TestX11SocketReadinessDoesNotOpenAClient(t *testing.T) {
	path := tempSocketPath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	if err = waitForX11Socket(path, time.Second); err != nil {
		t.Fatal(err)
	}
	if err = listener.SetDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.AcceptUnix()
	if err == nil {
		connection.Close()
		t.Fatal("X11 readiness opened a client connection")
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("accept: %v", err)
	}
}

func TestX11BridgeStartsAReachablePrivateDisplay(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" || !socketIsLive(waylandSocketPath(strconv.Itoa(os.Getuid()))) {
		t.Skip("no Wayland display")
	}
	if _, err := exec.LookPath("Xwayland"); err != nil {
		t.Skip("Xwayland is not installed")
	}
	state := t.TempDir()
	container, err := startX11Bridge(types.Container{StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(state, "x11.log"))
		t.Fatalf("%v\n%s", err, log)
	}
	t.Cleanup(func() { cleanupX11Bridge(container) })
	if container.X11Display == "" || !containerX11BridgeAlive(container) {
		t.Fatalf("X11 bridge is not reachable: %+v", container)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err = authenticateX11Socket(container.X11SocketPath, container.X11AuthorityPath, container.X11Display); err != nil {
			log, _ := os.ReadFile(container.LogPath)
			t.Fatalf("authenticate to private X11 display after %d completed clients: %v\n%s", attempt, err, log)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !containerX11BridgeAlive(container) {
		t.Fatal("private X11 display exited after its clients disconnected")
	}
}

func TestX11BrokerStopsTheContainerAfterItsLastWindowCloses(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" || !socketIsLive(waylandSocketPath(strconv.Itoa(os.Getuid()))) {
		t.Skip("no Wayland display")
	}
	if _, err := exec.LookPath("Xwayland"); err != nil {
		t.Skip("Xwayland is not installed")
	}
	state := t.TempDir()
	container, err := startX11Bridge(types.Container{CpakId: "broker-test", StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupX11Bridge(container) })
	process := exec.Command("sleep", "30")
	process.Env = append(os.Environ(), "CPAK_CONTAINER_ID="+container.CpakId)
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	processExited := make(chan error, 1)
	go func() { processExited <- process.Wait() }()
	t.Cleanup(func() {
		if process.Process != nil {
			_ = process.Process.Kill()
		}
	})
	container.Pid = process.Process.Pid
	container.ProcessStartTime, err = processStartTime(container.Pid)
	if err != nil {
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readyFD := duplicateTestFileDescriptor(t, readyWriter)
	done := make(chan error, 1)
	go func() {
		done <- RunX11Broker(X11BrokerOptions{
			NestedDisplay: x11BrokerDisplay(container), NestedAuthority: container.X11AuthorityPath,
			ServerPid: container.X11BridgePid, ServerStartTime: container.X11BridgeStartTime,
			ContainerPid: container.Pid, ContainerStartTime: container.ProcessStartTime,
			ContainerID: container.CpakId, ReadyFD: readyFD,
		})
	}()
	ready := []byte{0}
	if _, err = io.ReadFull(readyReader, ready); err != nil || ready[0] != 1 {
		t.Fatalf("broker readiness: %v, %v", ready, err)
	}
	readyReader.Close()
	previousAuthority, hadAuthority := os.LookupEnv("XAUTHORITY")
	t.Setenv("XAUTHORITY", container.X11AuthorityPath)
	connection, err := xgb.NewConnDisplay(x11BrokerDisplay(container))
	if hadAuthority {
		_ = os.Setenv("XAUTHORITY", previousAuthority)
	} else {
		_ = os.Unsetenv("XAUTHORITY")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	screen := xproto.Setup(connection).DefaultScreen(connection)
	tiny, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(connection, screen.RootDepth, tiny, screen.Root, 0, 0, 8, 8, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	xproto.MapWindow(connection, tiny)
	connection.Sync()
	time.Sleep(2300 * time.Millisecond)
	if !sameContainerProcess(container, container.Pid) {
		t.Fatal("a technical startup window stopped the container")
	}
	xproto.DestroyWindow(connection, tiny)
	connection.Sync()
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(connection, screen.RootDepth, window, screen.Root, 0, 0, 640, 480, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	xproto.MapWindow(connection, window)
	connection.Sync()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		attributes, attributesErr := xproto.GetWindowAttributes(connection, window).Reply()
		if attributesErr == nil && attributes.MapState == xproto.MapStateViewable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	xproto.DestroyWindow(connection, window)
	connection.Sync()
	select {
	case waitErr := <-processExited:
		if waitErr == nil {
			t.Fatal("container process exited without the expected termination signal")
		}
		if exit, ok := waitErr.(*exec.ExitError); !ok || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Fatalf("container process exit: %v", waitErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("container process survived the last X11 window")
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("broker exit: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("X11 broker survived its display")
	}
}

func duplicateTestFileDescriptor(t *testing.T, file *os.File) int {
	t.Helper()
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	return fd
}

func TestX11BridgeStartsAReachableDisplayOnX11(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no X11 display")
	}
	if _, err := exec.LookPath("Xephyr"); err != nil {
		t.Skip("Xephyr is not installed")
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	state := t.TempDir()
	container, err := startX11Bridge(types.Container{StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{})
	if err != nil {
		log, _ := os.ReadFile(filepath.Join(state, "x11.log"))
		t.Fatalf("%v\n%s", err, log)
	}
	t.Cleanup(func() { cleanupX11Bridge(container) })
	if container.X11Display == "" || !containerX11BridgeAlive(container) {
		t.Fatalf("X11 bridge is not reachable: %+v", container)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err = authenticateX11Socket(container.X11SocketPath, container.X11AuthorityPath, container.X11Display); err != nil {
			log, _ := os.ReadFile(container.LogPath)
			t.Fatalf("authenticate to private X11 display after %d completed clients: %v\n%s", attempt, err, log)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !containerX11BridgeAlive(container) {
		t.Fatal("private X11 display exited after its clients disconnected")
	}
}

func readX11AuthorityRecord(reader io.Reader) (uint16, [4][]byte, error) {
	var family uint16
	var fields [4][]byte
	if err := binary.Read(reader, binary.BigEndian, &family); err != nil {
		return 0, fields, err
	}
	for index := range fields {
		var length uint16
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return 0, fields, err
		}
		fields[index] = make([]byte, length)
		if _, err := io.ReadFull(reader, fields[index]); err != nil {
			return 0, fields, err
		}
	}
	return family, fields, nil
}

func authenticateX11Socket(socketPath, authorityPath, display string) error {
	encoded, err := os.ReadFile(authorityPath)
	if err != nil {
		return err
	}
	reader := bytes.NewReader(encoded)
	var cookie []byte
	for reader.Len() > 0 {
		_, fields, readErr := readX11AuthorityRecord(reader)
		if readErr != nil {
			return readErr
		}
		if string(fields[1]) == strings.TrimPrefix(display, ":") && string(fields[2]) == "MIT-MAGIC-COOKIE-1" {
			cookie = fields[3]
			break
		}
	}
	if len(cookie) == 0 {
		return errors.New("display has no X11 cookie")
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		return err
	}
	defer connection.Close()
	name := []byte("MIT-MAGIC-COOKIE-1")
	prefix := make([]byte, 12)
	prefix[0] = 'l'
	binary.LittleEndian.PutUint16(prefix[2:4], 11)
	binary.LittleEndian.PutUint16(prefix[6:8], uint16(len(name)))
	binary.LittleEndian.PutUint16(prefix[8:10], uint16(len(cookie)))
	request := append(prefix, name...)
	request = append(request, make([]byte, (4-len(name)%4)%4)...)
	request = append(request, cookie...)
	request = append(request, make([]byte, (4-len(cookie)%4)%4)...)
	if _, err = connection.Write(request); err != nil {
		return err
	}
	response := make([]byte, 8)
	if _, err = io.ReadFull(connection, response); err != nil {
		return err
	}
	if response[0] != 1 {
		return errors.New("X11 server rejected the private cookie")
	}
	return nil
}
