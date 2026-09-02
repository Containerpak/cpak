/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestX11BrokerIntegratesXephyrWithTheHostDesktop(t *testing.T) {
	if os.Getenv("DISPLAY") == "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("not an X11 session")
	}
	if _, err := exec.LookPath("Xephyr"); err != nil {
		t.Skip("Xephyr is not installed")
	}
	state := t.TempDir()
	container, err := startX11Bridge(types.Container{CpakId: "xephyr-broker-test", StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
	if err != nil {
		t.Fatal(err)
	}
	if container.X11HostWindowName == "" {
		t.Fatal("Xephyr has no stable host window identity")
	}
	process := exec.Command("sleep", "30")
	process.Env = append(os.Environ(), "CPAK_CONTAINER_ID="+container.CpakId)
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	container.Pid = process.Process.Pid
	container.ProcessStartTime, err = processStartTime(container.Pid)
	if err != nil {
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunX11Broker(X11BrokerOptions{
			NestedDisplay: x11BrokerDisplay(container), NestedAuthority: container.X11AuthorityPath,
			HostDisplay: os.Getenv("DISPLAY"), HostWindow: container.X11HostWindowName,
			ServerPid: container.X11BridgePid, ServerStartTime: container.X11BridgeStartTime,
			ContainerPid: container.Pid, ContainerStartTime: container.ProcessStartTime,
			ContainerID: container.CpakId, ReadyFD: int(readyWriter.Fd()), HostToApp: true, AppToHost: true,
		})
	}()
	ready := []byte{0}
	if _, err = io.ReadFull(readyReader, ready); err != nil {
		select {
		case brokerErr := <-done:
			t.Fatalf("X11 broker readiness: %v, broker: %v", err, brokerErr)
		case <-time.After(time.Second):
			t.Fatalf("X11 broker readiness: %v", err)
		}
	}
	if ready[0] != 1 {
		t.Fatalf("X11 broker readiness response: %v", ready)
	}
	readyReader.Close()
	defer func() {
		cleanupX11Bridge(container)
		if process.Process != nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("X11 broker did not stop")
		}
	}()

	host := connectHostX11(t)
	defer host.Close()
	outer := findTestClassWindow(t, host, container.X11HostWindowName)
	if outer == xproto.WindowNone {
		t.Fatal("Xephyr host window was not found by its private identity")
	}
	nested := connectTestX11(t, container)
	defer nested.Close()
	hostSource := connectHostX11(t)
	defer hostSource.Close()
	hostOwner := publishTestSelection(t, hostSource, "host clipboard through Xephyr")
	if got := readTestSelection(t, nested, xproto.WindowNone, "host clipboard through Xephyr"); got != "host clipboard through Xephyr" {
		t.Fatalf("host to application clipboard: %q", got)
	}
	nestedSource := connectTestX11(t, container)
	defer nestedSource.Close()
	publishTestSelection(t, nestedSource, "application clipboard through Xephyr")
	if got := readTestSelection(t, host, hostOwner, "application clipboard through Xephyr"); got != "application clipboard through Xephyr" {
		t.Fatalf("application to host clipboard: %q", got)
	}

	screen := xproto.Setup(nested).DefaultScreen(nested)
	window, err := xproto.NewWindowId(nested)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(nested, screen.RootDepth, window, screen.Root, 0, 0, 320, 240, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	name := testAtom(t, nested, "_NET_WM_NAME")
	utf8 := testAtom(t, nested, "UTF8_STRING")
	title := []byte("Xephyr browser")
	xproto.ChangeProperty(nested, xproto.PropModeReplace, window, name, utf8, 8, uint32(len(title)), title)
	xproto.MapWindow(nested, window)
	nested.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		return readTestProperty(t, host, outer, "_NET_WM_NAME") == string(title)
	}, "Xephyr title was not synchronized")

	_ = xproto.ConfigureWindowChecked(host, outer, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{1024, 700}).Check()
	host.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		root, rootErr := xproto.GetGeometry(nested, xproto.Drawable(screen.Root)).Reply()
		geometry, geometryErr := xproto.GetGeometry(nested, xproto.Drawable(window)).Reply()
		return rootErr == nil && geometryErr == nil && root.Width == 1024 && root.Height == 700 && geometry.Width == root.Width && geometry.Height == root.Height
	}, "Xephyr resize did not reach the application")

	stateAtom := testAtom(t, nested, "_NET_WM_STATE")
	fullscreen := testAtom(t, nested, "_NET_WM_STATE_FULLSCREEN")
	request := xproto.ClientMessageEvent{
		Format: 32, Window: window, Type: stateAtom,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{1, uint32(fullscreen), 0, 1, 0}),
	}
	xproto.SendEvent(nested, false, screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(request.Bytes()))
	nested.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		states := readTestAtoms(host, outer, "_NET_WM_STATE")
		return containsTestAtom(states, testAtom(t, host, "_NET_WM_STATE_FULLSCREEN"))
	}, "fullscreen did not reach the host window manager")

	xproto.DestroyWindow(nested, window)
	nested.Sync()
	waitTestCondition(t, 4*time.Second, func() bool {
		return !sameContainerProcess(container, container.Pid) && !sameRecordedProcess(container.X11BridgePid, container.X11BridgeStartTime)
	}, "closing the last window did not stop the cpak instance")
}

func connectHostX11(t *testing.T) *xgb.Conn {
	t.Helper()
	connection, err := xgb.NewConnDisplay(os.Getenv("DISPLAY"))
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func findTestClassWindow(t *testing.T, connection *xgb.Conn, class string) xproto.Window {
	t.Helper()
	root := xproto.Setup(connection).DefaultScreen(connection).Root
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		queue := []xproto.Window{root}
		for len(queue) > 0 {
			window := queue[0]
			queue = queue[1:]
			property, err := xproto.GetProperty(connection, false, window, xproto.AtomWmClass, xproto.AtomString, 0, 1024).Reply()
			if err == nil {
				for _, value := range splitTestClass(property.Value) {
					if value == class {
						return window
					}
				}
			}
			tree, err := xproto.QueryTree(connection, window).Reply()
			if err == nil {
				queue = append(queue, tree.Children...)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return xproto.WindowNone
}

func splitTestClass(value []byte) []string {
	result := []string{}
	start := 0
	for index, item := range value {
		if item == 0 {
			result = append(result, string(value[start:index]))
			start = index + 1
		}
	}
	if start < len(value) {
		result = append(result, string(value[start:]))
	}
	return result
}

func readTestAtoms(connection *xgb.Conn, window xproto.Window, name string) []xproto.Atom {
	atom, err := internTestAtom(connection, name)
	if err != nil {
		return nil
	}
	reply, err := xproto.GetProperty(connection, false, window, atom, xproto.AtomAtom, 0, 1024).Reply()
	if err != nil || reply.Format != 32 {
		return nil
	}
	result := make([]xproto.Atom, len(reply.Value)/4)
	for index := range result {
		result[index] = xproto.Atom(xgb.Get32(reply.Value[index*4:]))
	}
	return result
}

func containsTestAtom(atoms []xproto.Atom, wanted xproto.Atom) bool {
	for _, atom := range atoms {
		if atom == wanted {
			return true
		}
	}
	return false
}

func waitTestCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(message)
}
