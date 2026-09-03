/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestX11BrokerMovesClipboardTextInBothDirections(t *testing.T) {
	host, nested := testX11Displays(t)
	stop := startTestX11Broker(t, host, nested, true, true)
	defer stop()

	hostSource := connectTestX11(t, host)
	defer hostSource.Close()
	hostOwner := publishTestSelection(t, hostSource, "copied on host")
	nestedReader := connectTestX11(t, nested)
	defer nestedReader.Close()
	if got := readTestSelection(t, nestedReader, hostOwner, "copied on host"); got != "copied on host" {
		t.Fatalf("host to application clipboard: %q", got)
	}

	nestedSource := connectTestX11(t, nested)
	defer nestedSource.Close()
	nestedOwner := publishTestSelection(t, nestedSource, "copied in application")
	hostReader := connectTestX11(t, host)
	defer hostReader.Close()
	if got := readTestSelection(t, hostReader, hostOwner, "copied in application"); got != "copied in application" {
		t.Fatalf("application to host clipboard: %q", got)
	}

	large := strings.Repeat("clipboard-block-", 8_000)
	largeHostSource := connectTestX11(t, host)
	defer largeHostSource.Close()
	publishTestSelection(t, largeHostSource, large)
	if got := readTestSelection(t, nestedReader, nestedOwner, large); got != large {
		t.Fatalf("large clipboard transfer: got %d bytes, want %d", len(got), len(large))
	}
}

func TestX11BrokerDoesNotMoveClipboardWithoutDirections(t *testing.T) {
	host, nested := testX11Displays(t)
	stop := startTestX11Broker(t, host, nested, false, false)
	defer stop()

	hostSource := connectTestX11(t, host)
	defer hostSource.Close()
	publishTestSelection(t, hostSource, "must stay on host")
	time.Sleep(300 * time.Millisecond)
	nestedReader := connectTestX11(t, nested)
	defer nestedReader.Close()
	clipboard := testAtom(t, nestedReader, "CLIPBOARD")
	owner, err := xproto.GetSelectionOwner(nestedReader, clipboard).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if owner.Owner != xproto.WindowNone {
		t.Fatalf("clipboard crossed the disabled boundary through window %d", owner.Owner)
	}
}

func TestX11BrokerMirrorsWindowIdentityAndFullscreen(t *testing.T) {
	host, nested := testX11Displays(t)
	hostWM := connectTestX11(t, host)
	defer hostWM.Close()
	hostScreen := xproto.Setup(hostWM).DefaultScreen(hostWM)
	if err := xproto.ChangeWindowAttributesChecked(hostWM, hostScreen.Root, xproto.CwEventMask, []uint32{xproto.EventMaskSubstructureRedirect | xproto.EventMaskSubstructureNotify}).Check(); err != nil {
		t.Fatal(err)
	}
	outer, err := xproto.NewWindowId(hostWM)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(hostWM, hostScreen.RootDepth, outer, hostScreen.Root, 0, 0, 800, 600, 0, xproto.WindowClassInputOutput, hostScreen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	class := []byte("missing-test-window\x00missing-test-window\x00")
	xproto.ChangeProperty(hostWM, xproto.PropModeReplace, outer, xproto.AtomWmClass, xproto.AtomString, 8, uint32(len(class)), class)
	xproto.MapWindow(hostWM, outer)
	hostWM.Sync()
	stop := startTestX11Broker(t, host, nested, false, false)
	defer stop()

	application := connectTestX11(t, nested)
	defer application.Close()
	screen := xproto.Setup(application).DefaultScreen(application)
	window, err := xproto.NewWindowId(application)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(application, screen.RootDepth, window, screen.Root, 20, 30, 320, 240, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	name := testAtom(t, application, "_NET_WM_NAME")
	utf8 := testAtom(t, application, "UTF8_STRING")
	icon := testAtom(t, application, "_NET_WM_ICON")
	title := []byte("Browser window")
	xproto.ChangeProperty(application, xproto.PropModeReplace, window, name, utf8, 8, uint32(len(title)), title)
	iconData := make([]byte, 12)
	xgb.Put32(iconData[0:4], 1)
	xgb.Put32(iconData[4:8], 1)
	xgb.Put32(iconData[8:12], 0xff336699)
	xproto.ChangeProperty(application, xproto.PropModeReplace, window, icon, xproto.AtomCardinal, 32, 3, iconData)
	xproto.MapWindow(application, window)
	application.Sync()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		geometry, geometryErr := xproto.GetGeometry(application, xproto.Drawable(window)).Reply()
		root, rootErr := xproto.GetGeometry(application, xproto.Drawable(screen.Root)).Reply()
		titleReply, titleErr := xproto.GetProperty(hostWM, false, outer, testAtom(t, hostWM, "_NET_WM_NAME"), xproto.GetPropertyTypeAny, 0, 1024).Reply()
		iconReply, iconErr := xproto.GetProperty(hostWM, false, outer, testAtom(t, hostWM, "_NET_WM_ICON"), xproto.GetPropertyTypeAny, 0, 1024).Reply()
		if geometryErr == nil && rootErr == nil && geometry.Width == root.Width && geometry.Height == root.Height && titleErr == nil && string(titleReply.Value) == string(title) && iconErr == nil && string(iconReply.Value) == string(iconData) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	geometry, err := xproto.GetGeometry(application, xproto.Drawable(window)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	root, err := xproto.GetGeometry(application, xproto.Drawable(screen.Root)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if geometry.Width != root.Width || geometry.Height != root.Height {
		t.Fatalf("application geometry: %dx%d, display: %dx%d", geometry.Width, geometry.Height, root.Width, root.Height)
	}
	if got := readTestProperty(t, hostWM, outer, "_NET_WM_NAME"); got != string(title) {
		t.Fatalf("outer window title: %q", got)
	}
	if got := readTestProperty(t, hostWM, outer, "_NET_WM_ICON"); got != string(iconData) {
		t.Fatalf("outer window icon: %x", []byte(got))
	}

	state := testAtom(t, application, "_NET_WM_STATE")
	fullscreen := testAtom(t, application, "_NET_WM_STATE_FULLSCREEN")
	request := xproto.ClientMessageEvent{
		Format: 32, Window: window, Type: state,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{1, uint32(fullscreen), 0, 1, 0}),
	}
	xproto.SendEvent(application, false, screen.Root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(request.Bytes()))
	application.Sync()
	fullscreenHost := testAtom(t, hostWM, "_NET_WM_STATE_FULLSCREEN")
	stateHost := testAtom(t, hostWM, "_NET_WM_STATE")
	events := make(chan xgb.Event, 1)
	go func() {
		for {
			event, eventErr := hostWM.WaitForEvent()
			if eventErr != nil || event == nil {
				return
			}
			message, ok := event.(xproto.ClientMessageEvent)
			if ok && message.Window == outer && message.Type == stateHost && len(message.Data.Data32) >= 2 && message.Data.Data32[0] == 1 && xproto.Atom(message.Data.Data32[1]) == fullscreenHost {
				events <- event
				return
			}
		}
	}()
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("fullscreen request did not reach the host window manager")
	}
}

func testX11Displays(t *testing.T) (types.Container, types.Container) {
	t.Helper()
	if os.Getenv("WAYLAND_DISPLAY") == "" || !socketIsLive(waylandSocketPath(strconv.Itoa(os.Getuid()))) {
		t.Skip("no Wayland display")
	}
	if _, err := exec.LookPath("Xwayland"); err != nil {
		t.Skip("Xwayland is not installed")
	}
	start := func(id string) types.Container {
		state := t.TempDir()
		container, err := startX11Bridge(types.Container{CpakId: id, StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupX11Bridge(container) })
		return container
	}
	return start("clipboard-host"), start("clipboard-guest")
}

func startTestX11Broker(t *testing.T, host, nested types.Container, hostToApp, appToHost bool) func() {
	t.Helper()
	process := exec.Command("sleep", "30")
	process.Env = append(os.Environ(), "CPAK_CONTAINER_ID="+nested.CpakId)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	started, err := processStartTime(process.Process.Pid)
	if err != nil {
		process.Process.Kill()
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		process.Process.Kill()
		t.Fatal(err)
	}
	readyFD := duplicateTestFileDescriptor(t, readyWriter)
	previousAuthority, hadAuthority := os.LookupEnv("XAUTHORITY")
	if err = os.Setenv("XAUTHORITY", host.X11AuthorityPath); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- RunX11Broker(X11BrokerOptions{
			NestedDisplay: x11BrokerDisplay(nested), NestedAuthority: nested.X11AuthorityPath,
			HostDisplay: x11BrokerDisplay(host), HostWindow: "missing-test-window",
			ServerPid: nested.X11BridgePid, ServerStartTime: nested.X11BridgeStartTime,
			ContainerPid: process.Process.Pid, ContainerStartTime: started, ContainerID: nested.CpakId,
			ReadyFD: readyFD, HostToApp: hostToApp, AppToHost: appToHost,
		})
	}()
	ready := []byte{0}
	if _, err = io.ReadFull(readyReader, ready); err != nil || ready[0] != 1 {
		t.Fatalf("X11 broker readiness: %v, %v", ready, err)
	}
	readyReader.Close()
	if hadAuthority {
		_ = os.Setenv("XAUTHORITY", previousAuthority)
	} else {
		_ = os.Unsetenv("XAUTHORITY")
	}
	return func() {
		cleanupX11Bridge(nested)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("X11 broker did not stop with its display")
		}
		if process.Process != nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	}
}

func connectTestX11(t *testing.T, container types.Container) *xgb.Conn {
	t.Helper()
	previous, present := os.LookupEnv("XAUTHORITY")
	if err := os.Setenv("XAUTHORITY", container.X11AuthorityPath); err != nil {
		t.Fatal(err)
	}
	connection, err := xgb.NewConnDisplay(x11BrokerDisplay(container))
	if present {
		_ = os.Setenv("XAUTHORITY", previous)
	} else {
		_ = os.Unsetenv("XAUTHORITY")
	}
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func publishTestSelection(t *testing.T, connection *xgb.Conn, text string) xproto.Window {
	t.Helper()
	screen := xproto.Setup(connection).DefaultScreen(connection)
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(connection, screen.RootDepth, window, screen.Root, 0, 0, 1, 1, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	clipboard := testAtom(t, connection, "CLIPBOARD")
	xproto.SetSelectionOwner(connection, window, clipboard, xproto.TimeCurrentTime)
	connection.Sync()
	go func() {
		targets, _ := internTestAtom(connection, "TARGETS")
		utf8, _ := internTestAtom(connection, "UTF8_STRING")
		for {
			event, eventErr := connection.WaitForEvent()
			if eventErr != nil || event == nil {
				return
			}
			request, ok := event.(xproto.SelectionRequestEvent)
			if !ok {
				continue
			}
			property := request.Property
			if property == xproto.AtomNone {
				property = request.Target
			}
			delivered := property
			incremental := false
			switch request.Target {
			case targets:
				data := make([]byte, 4)
				xgb.Put32(data, uint32(utf8))
				xproto.ChangeProperty(connection, xproto.PropModeReplace, request.Requestor, property, xproto.AtomAtom, 32, 1, data)
			case utf8:
				data := []byte(text)
				if len(data) > 64<<10 {
					incr, _ := internTestAtom(connection, "INCR")
					size := make([]byte, 4)
					xgb.Put32(size, uint32(len(data)))
					xproto.ChangeProperty(connection, xproto.PropModeReplace, request.Requestor, property, incr, 32, 1, size)
					incremental = true
				} else {
					xproto.ChangeProperty(connection, xproto.PropModeReplace, request.Requestor, property, utf8, 8, uint32(len(data)), data)
				}
			default:
				delivered = xproto.AtomNone
			}
			notify := xproto.SelectionNotifyEvent{Time: request.Time, Requestor: request.Requestor, Selection: request.Selection, Target: request.Target, Property: delivered}
			xproto.SendEvent(connection, false, request.Requestor, xproto.EventMaskNoEvent, string(notify.Bytes()))
			connection.Sync()
			if incremental {
				go sendTestSelectionIncremental(connection, request.Requestor, property, utf8, []byte(text))
			}
		}
	}()
	return window
}

func sendTestSelectionIncremental(connection *xgb.Conn, requestor xproto.Window, property, kind xproto.Atom, data []byte) {
	waitMissing := func() bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			reply, err := xproto.GetProperty(connection, false, requestor, property, xproto.GetPropertyTypeAny, 0, 0).Reply()
			if err != nil {
				return false
			}
			if reply.Type == xproto.AtomNone {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	if !waitMissing() {
		return
	}
	for offset := 0; offset < len(data); offset += 32 << 10 {
		end := offset + 32<<10
		if end > len(data) {
			end = len(data)
		}
		part := data[offset:end]
		xproto.ChangeProperty(connection, xproto.PropModeReplace, requestor, property, kind, 8, uint32(len(part)), part)
		connection.Sync()
		if !waitMissing() {
			return
		}
	}
	xproto.ChangeProperty(connection, xproto.PropModeReplace, requestor, property, kind, 8, 0, nil)
	connection.Sync()
}

func readTestSelection(t *testing.T, connection *xgb.Conn, previousOwner xproto.Window, expected string) string {
	t.Helper()
	value, ok := tryReadTestSelection(t, connection, previousOwner, 3*time.Second)
	if !ok {
		t.Fatalf("clipboard value with %d bytes was not delivered", len(expected))
	}
	return value
}

func tryReadTestSelection(t *testing.T, connection *xgb.Conn, previousOwner xproto.Window, timeout time.Duration) (string, bool) {
	t.Helper()
	clipboard := testAtom(t, connection, "CLIPBOARD")
	utf8 := testAtom(t, connection, "UTF8_STRING")
	property := testAtom(t, connection, "CPAK_TEST_SELECTION")
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	screen := xproto.Setup(connection).DefaultScreen(connection)
	if err = xproto.CreateWindowChecked(connection, screen.RootDepth, window, screen.Root, 0, 0, 1, 1, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	defer xproto.DestroyWindow(connection, window)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		owner, ownerErr := xproto.GetSelectionOwner(connection, clipboard).Reply()
		if ownerErr == nil && owner.Owner != xproto.WindowNone && owner.Owner != previousOwner {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	xproto.DeleteProperty(connection, window, property)
	xproto.ConvertSelection(connection, window, clipboard, utf8, property, xproto.TimeCurrentTime)
	connection.Sync()
	incr := testAtom(t, connection, "INCR")
	value := []byte{}
	incremental := false
	for time.Now().Before(deadline) {
		reply, propertyErr := xproto.GetProperty(connection, true, window, property, xproto.GetPropertyTypeAny, 0, 4<<20).Reply()
		if propertyErr == nil && reply.Type != xproto.AtomNone {
			if reply.Type == incr {
				incremental = true
				time.Sleep(20 * time.Millisecond)
				continue
			}
			if !incremental {
				return string(reply.Value), true
			}
			if len(reply.Value) == 0 {
				return string(value), true
			}
			value = append(value, reply.Value...)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", false
}

func testAtom(t *testing.T, connection *xgb.Conn, name string) xproto.Atom {
	t.Helper()
	atom, err := internTestAtom(connection, name)
	if err != nil {
		t.Fatal(err)
	}
	return atom
}

func readTestProperty(t *testing.T, connection *xgb.Conn, window xproto.Window, name string) string {
	t.Helper()
	reply, err := xproto.GetProperty(connection, false, window, testAtom(t, connection, name), xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
	if err != nil {
		t.Fatal(err)
	}
	return string(reply.Value)
}

func internTestAtom(connection *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(connection, false, uint16(len(name)), name).Reply()
	if err != nil {
		return xproto.AtomNone, err
	}
	if reply.Atom == xproto.AtomNone {
		return xproto.AtomNone, errors.New("X11 server returned an empty atom")
	}
	return reply.Atom, nil
}
