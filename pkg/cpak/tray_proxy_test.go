/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb/xproto"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type trayTestWatcher struct{ registered chan string }

func (w *trayTestWatcher) RegisterStatusNotifierItem(name string) *dbus.Error {
	w.registered <- name
	return nil
}

func TestX11TraySurvivesHiddenWindowsAndRestoresThem(t *testing.T) {
	if os.Getenv("DISPLAY") == "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		t.Skip("requires a private X11 test session")
	}
	if _, err := exec.LookPath("Xephyr"); err != nil {
		t.Skip("Xephyr unavailable")
	}
	testTrayBroker(t, false)
}
func TestXwaylandTraySurvivesHiddenWindowsAndRestoresThem(t *testing.T) {
	if os.Getenv("CPAK_WAYLAND_INPUT_TEST") != "1" {
		t.Skip("private Wayland test disabled")
	}
	if _, err := exec.LookPath("grim"); err != nil {
		t.Fatal("grim is required for the private Wayland display test")
	}
	testTrayBroker(t, true)
}

func testTrayBroker(t *testing.T, wayland bool) {
	t.Helper()
	var background image.Image
	if wayland {
		waitTestCondition(t, 3*time.Second, func() bool {
			background = trayTestScreenshot(t)
			bounds := background.Bounds()
			red, green, blue, _ := background.At(bounds.Min.X+8, bounds.Min.Y+8).RGBA()
			t.Logf("private compositor background: #%02x%02x%02x", red>>8, green>>8, blue>>8)
			return red>>8 == 0x28 && green>>8 == 0x46 && blue>>8 == 0x68
		}, "private compositor background did not render")
	}
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon unavailable")
	}
	state := t.TempDir()
	address := "unix:path=" + filepath.Join(state, "bus")
	busProcess := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address="+address)
	if err := busProcess.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busProcess.Process.Kill(); _ = busProcess.Wait() })
	waitTestCondition(t, 2*time.Second, func() bool { _, err := os.Stat(filepath.Join(state, "bus")); return err == nil }, "private bus unavailable")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
	bus, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	watcher := &trayTestWatcher{registered: make(chan string, 4)}
	if err = bus.Export(watcher, "/StatusNotifierWatcher", "org.kde.StatusNotifierWatcher"); err != nil {
		t.Fatal(err)
	}
	if _, err = bus.RequestName("org.kde.StatusNotifierWatcher", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	container, runtime, err := startX11Bridge(types.Container{CpakId: "tray-lifecycle", StatePath: state, LogPath: filepath.Join(state, "x11.log")}, types.ClipboardGrant{HostToApp: true, AppToHost: true})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	t.Cleanup(func() { cleanupX11Bridge(container) })
	process := exec.Command("sleep", "30")
	process.Env = append(os.Environ(), "CPAK_CONTAINER_ID="+container.CpakId)
	if err = process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _, _ = process.Process.Wait() })
	container.Pid = process.Process.Pid
	container.ProcessStartTime, err = processStartTime(container.Pid)
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	options := X11BrokerOptions{NestedDisplay: x11BrokerDisplay(container), NestedAuthority: container.X11AuthorityPath, ContainerPid: container.Pid, ContainerStartTime: container.ProcessStartTime, ContainerID: container.CpakId, ReadyFD: duplicateTestFileDescriptor(t, writer), TrayProxy: "xembed", TrayName: "Tray fixture"}
	if wayland {
		options.ListenFD = duplicateTestFileDescriptor(t, runtime.listener)
		options.ListenPath = runtime.listenPath
		options.X11Server = runtime.x11Server
	} else {
		options.HostDisplay = os.Getenv("DISPLAY")
		options.HostWindow = container.X11HostWindowName
		options.ServerPid = container.X11BridgePid
		options.ServerStartTime = container.X11BridgeStartTime
	}
	done := make(chan error, 1)
	go func() { done <- RunX11Broker(options) }()
	t.Cleanup(func() {
		_ = process.Process.Kill()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("tray broker did not stop")
		}
	})
	var ready [1]byte
	if _, err = io.ReadFull(reader, ready[:]); err != nil {
		t.Fatal(err)
	}
	client := connectTestX11(t, container)
	defer client.Close()
	screen := xproto.Setup(client).DefaultScreen(client)
	selection := testAtom(t, client, "_NET_SYSTEM_TRAY_S0")
	var owner xproto.Window
	waitTestCondition(t, 2*time.Second, func() bool {
		reply, err := xproto.GetSelectionOwner(client, selection).Reply()
		if err == nil {
			owner = reply.Owner
		}
		return owner != 0
	}, "broker did not claim the tray")
	icon, err := xproto.NewWindowId(client)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(client, screen.RootDepth, icon, screen.Root, 0, 0, 32, 32, 0, xproto.WindowClassInputOutput, screen.RootVisual, xproto.CwBackPixel|xproto.CwEventMask, []uint32{0x40a080, xproto.EventMaskButtonPress}).Check(); err != nil {
		t.Fatal(err)
	}
	info := testAtom(t, client, "_XEMBED_INFO")
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[4:], 1)
	xproto.ChangeProperty(client, xproto.PropModeReplace, icon, info, info, 32, 2, data)
	opcode := testAtom(t, client, "_NET_SYSTEM_TRAY_OPCODE")
	message := xproto.ClientMessageEvent{Window: owner, Type: opcode, Format: 32, Data: xproto.ClientMessageDataUnionData32New([]uint32{0, 0, uint32(icon), 0, 0})}
	if err = xproto.SendEventChecked(client, false, owner, 0, string(message.Bytes())).Check(); err != nil {
		t.Fatal(err)
	}
	var service string
	select {
	case service = <-watcher.registered:
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not register the tray icon")
	}
	window, err := xproto.NewWindowId(client)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(client, screen.RootDepth, window, screen.Root, 0, 0, 320, 240, 0, xproto.WindowClassInputOutput, screen.RootVisual, xproto.CwBackPixel, []uint32{0x40a080}).Check(); err != nil {
		t.Fatal(err)
	}
	visible := func() bool {
		if wayland {
			return !reflect.DeepEqual(background, trayTestScreenshot(t))
		}
		host := connectHostX11(t)
		defer host.Close()
		outer := findTestClassWindow(t, host, container.X11HostWindowName)
		if outer == 0 {
			return false
		}
		attrs, err := xproto.GetWindowAttributes(host, outer).Reply()
		return err == nil && attrs.MapState == xproto.MapStateViewable
	}
	xproto.MapWindow(client, window)
	client.Sync()
	waitTestCondition(t, 3*time.Second, func() bool {
		attrs, err := xproto.GetWindowAttributes(client, window).Reply()
		return err == nil && attrs.MapState == xproto.MapStateViewable && visible()
	}, "application window did not appear")
	xproto.UnmapWindow(client, window)
	client.Sync()
	waitTestCondition(t, 3*time.Second, func() bool { return !visible() }, "tray-only application left an empty desktop window")
	time.Sleep(2300 * time.Millisecond)
	if !sameContainerProcess(container, container.Pid) {
		t.Fatal("minimizing to tray stopped the application")
	}
	events := make(chan bool, 1)
	go func() {
		for {
			event, err := client.WaitForEvent()
			if err != nil || event == nil {
				events <- false
				return
			}
			if press, ok := event.(xproto.ButtonPressEvent); ok && press.Event == icon {
				xproto.MapWindow(client, window)
				client.Sync()
				events <- true
				return
			}
		}
	}()
	if err = bus.Object(service, "/StatusNotifierItem").Call("org.kde.StatusNotifierItem.Activate", 0, int32(50), int32(50)).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-events:
		if !ok {
			t.Fatal("activation lost the private display")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("activation did not reach the application")
	}
	waitTestCondition(t, 3*time.Second, visible, "tray activation did not restore the application window")
	for cycle := 0; cycle < 2; cycle++ {
		xproto.UnmapWindow(client, window)
		client.Sync()
		waitTestCondition(t, 3*time.Second, func() bool { return !visible() }, "repeated tray minimize left a window")
		xproto.MapWindow(client, window)
		client.Sync()
		waitTestCondition(t, 3*time.Second, visible, "repeated tray restore lost its window")
	}
	xproto.DestroyWindow(client, window)
	xproto.DestroyWindow(client, icon)
	client.Sync()
	waitTestCondition(t, 5*time.Second, func() bool { return !sameContainerProcess(container, container.Pid) }, "last icon and window did not release the application")
}

// Older compositors can retain foreign-toplevel handles after a surface unmaps.
// Compare rendered pixels so the test checks what remains on the desktop.
func trayTestScreenshot(t *testing.T) image.Image {
	t.Helper()
	data, err := exec.Command("grim", "-t", "png", "-").Output()
	if err != nil {
		t.Fatalf("capture private tray display: %v", err)
	}
	screenshot, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return screenshot
}
