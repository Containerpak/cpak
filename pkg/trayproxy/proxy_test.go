/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trayproxy

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type watcher struct{ registered chan string }

func (w *watcher) RegisterStatusNotifierItem(name string) *dbus.Error {
	w.registered <- name
	return nil
}

func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon is unavailable")
	}
	dir, err := os.MkdirTemp("", "tray-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "bus.sock")
	command := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=unix:path="+path)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return "unix:path=" + path
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("private bus did not start")
	return ""
}

func testX(t *testing.T) *xgb.Conn {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no private X11 test display")
	}
	connection, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(connection.Close)
	return connection
}

func testWatcher(t *testing.T, address string) (*dbus.Conn, *watcher) {
	t.Helper()
	connection, err := dbus.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	handler := &watcher{registered: make(chan string, maxIcons)}
	if err := connection.Export(handler, "/StatusNotifierWatcher", watcherNames[0]); err != nil {
		t.Fatal(err)
	}
	if reply, err := connection.RequestName(watcherNames[0], dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own watcher: %v %v", reply, err)
	}
	return connection, handler
}

func dockTestIcon(t *testing.T, connection *xgb.Conn, p *Proxy, color uint32) xproto.Window {
	t.Helper()
	screen := xproto.Setup(connection).DefaultScreen(connection)
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		t.Fatal(err)
	}
	if err := xproto.CreateWindowChecked(connection, screen.RootDepth, window, screen.Root, 0, 0, iconSize, iconSize, 0, xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwBackPixel|xproto.CwEventMask, []uint32{color, xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease | xproto.EventMaskStructureNotify}).Check(); err != nil {
		t.Fatal(err)
	}
	writeProperty(connection, window, p.atoms.info, p.atoms.info, 0, 1)
	message := xproto.ClientMessageEvent{Format: 32, Window: p.owner, Type: p.atoms.opcode, Data: xproto.ClientMessageDataUnionData32New([]uint32{0, 0, uint32(window), 0, 0})}
	if err := xproto.SendEventChecked(connection, false, p.owner, 0, string(message.Bytes())).Check(); err != nil {
		t.Fatal(err)
	}
	return window
}

func registeredItem(t *testing.T, w *watcher) string {
	t.Helper()
	select {
	case name := <-w.registered:
		return name
	case <-time.After(4 * time.Second):
		t.Fatal("icon was not registered")
		return ""
	}
}

func readProperty(t *testing.T, connection *dbus.Conn, name, property string) dbus.Variant {
	t.Helper()
	var value dbus.Variant
	if err := connection.Object(name, itemPath).Call("org.freedesktop.DBus.Properties.Get", 0, itemInterfaces[0], property).Store(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestTrayExportsIconsAndControlsToARealWatcher(t *testing.T) {
	connection := testX(t)
	address := privateBus(t)
	bus, host := testWatcher(t, address)
	proxy, err := New(Options{Connection: connection, BusAddress: address, Name: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- proxy.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("tray proxy did not stop")
		}
	})
	client := testX(t)
	buttons := make(chan xproto.ButtonPressEvent, 16)
	go func() {
		defer close(buttons)
		for {
			event, err := client.WaitForEvent()
			if event == nil && err == nil {
				return
			}
			if press, ok := event.(xproto.ButtonPressEvent); ok {
				select {
				case buttons <- press:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	first := dockTestIcon(t, client, proxy, 0xff0000)
	name := registeredItem(t, host)
	var images []pixmap
	if err := dbus.Store([]any{readProperty(t, bus, name, "IconPixmap").Value()}, &images); err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Width != iconSize || len(images[0].Data) != iconSize*iconSize*4 || binary.BigEndian.Uint32(images[0].Data[:4]) != 0xffff0000 {
		t.Fatalf("unexpected icon pixels: %+v", images)
	}
	for method, button := range map[string]xproto.Button{"Activate": 1, "ContextMenu": 3, "SecondaryActivate": 2} {
		if err := bus.Object(name, itemPath).Call(itemInterfaces[0]+"."+method, 0, int32(10), int32(20)).Err; err != nil {
			t.Fatal(err)
		}
		select {
		case press := <-buttons:
			if press.Event != first || press.Detail != button {
				t.Fatalf("button callback: %+v", press)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("tray activation did not reach the XEmbed icon")
		}
	}
	if err := bus.Object(name, itemPath).Call("org.freedesktop.DBus.Properties.Set", 0, itemInterfaces[0], "Title", dbus.MakeVariant("changed by caller")).Err; err == nil {
		t.Fatal("host changed a read-only tray property")
	}
	title := []byte("Updated tray")
	xproto.ChangeProperty(client, xproto.PropModeReplace, first, proxy.atoms.name, proxy.atoms.utf8, 8, uint32(len(title)), title)
	_ = xproto.ChangeWindowAttributesChecked(client, first, xproto.CwBackPixel, []uint32{0x0000ff}).Check()
	_ = xproto.ClearAreaChecked(client, false, first, 0, 0, iconSize, iconSize).Check()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readProperty(t, bus, name, "Title").Value() == string(title) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readProperty(t, bus, name, "Title").Value() != string(title) {
		t.Fatal("tray title did not update")
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = dbus.Store([]any{readProperty(t, bus, name, "IconPixmap").Value()}, &images)
		if len(images) == 1 && binary.BigEndian.Uint32(images[0].Data[:4]) == 0xff0000ff {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(images) != 1 || binary.BigEndian.Uint32(images[0].Data[:4]) != 0xff0000ff {
		t.Fatal("tray pixels did not update")
	}
	writeProperty(client, first, proxy.atoms.info, proxy.atoms.info, 0, 0)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if readProperty(t, bus, name, "Status").Value() == "Passive" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if readProperty(t, bus, name, "Status").Value() != "Passive" {
		t.Fatal("XEmbed mapped flag was ignored")
	}
	attrs, err := xproto.GetWindowAttributes(client, first).Reply()
	if err != nil || attrs.MapState != xproto.MapStateUnmapped {
		t.Fatal("unmapped XEmbed icon stayed visible")
	}
	writeProperty(client, first, proxy.atoms.info, proxy.atoms.info, 0, 1)
	if err := bus.Object(name, itemPath).Call(itemInterfaces[0]+".Scroll", 0, int32(-120), "horizontal").Err; err != nil {
		t.Fatal(err)
	}
	select {
	case press := <-buttons:
		if press.Detail != 7 {
			t.Fatalf("horizontal scroll button: %d", press.Detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scroll did not reach XEmbed")
	}
	if err := bus.Object(name, itemPath).Call(itemInterfaces[0]+".Scroll", 0, int32(120), "diagonal").Err; err == nil {
		t.Fatal("invalid scroll orientation was accepted")
	}
	second := dockTestIcon(t, client, proxy, 0x00ff00)
	other := registeredItem(t, host)
	if other == name {
		t.Fatal("two icons shared their service identity")
	}
	var otherImages []pixmap
	if err := dbus.Store([]any{readProperty(t, bus, other, "IconPixmap").Value()}, &otherImages); err != nil {
		t.Fatal(err)
	}
	if len(otherImages) != 1 || binary.BigEndian.Uint32(otherImages[0].Data[:4]) != 0xff00ff00 {
		t.Fatal("the second icon did not keep its own pixels")
	}
	if _, err := bus.ReleaseName(watcherNames[0]); err != nil {
		t.Fatal(err)
	}
	_, replacement := testWatcher(t, address)
	seen := map[string]bool{registeredItem(t, replacement): true, registeredItem(t, replacement): true}
	if !seen[name] || !seen[other] {
		t.Fatal("watcher restart lost an icon")
	}
	if err := xproto.DestroyWindowChecked(client, first).Check(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var present bool
		_ = bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&present)
		if !present {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var present bool
	_ = bus.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&present)
	if present {
		t.Fatal("destroyed icon left a service registered")
	}
	if !proxy.Active() {
		t.Fatal("removing one icon removed the other")
	}
	if err := xproto.DestroyWindowChecked(client, second).Check(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for proxy.Active() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.Active() {
		t.Fatal("the last destroyed icon kept the app alive")
	}
}

func TestTrayWithoutAWatcherStaysLocalAndBoundsTheIconCount(t *testing.T) {
	connection := testX(t)
	proxy, err := New(Options{Connection: connection, Name: "Offline tray"})
	if err != nil {
		t.Fatal(err)
	}
	other := testX(t)
	if _, err = New(Options{Connection: other}); err == nil {
		t.Fatal("another tray replaced the active manager")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- proxy.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("offline tray did not stop")
		}
	})
	client := testX(t)
	var overflow xproto.Window
	for n := 0; n <= maxIcons; n++ {
		overflow = dockTestIcon(t, client, proxy, 0x123456)
	}
	deadline := time.Now().Add(3 * time.Second)
	for proxy.active.Load() != maxIcons && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.active.Load() != maxIcons {
		t.Fatalf("docked %d icons, expected %d", proxy.active.Load(), maxIcons)
	}
	tree, err := xproto.QueryTree(client, overflow).Reply()
	if err != nil || tree.Parent != proxy.screen.Root {
		t.Fatal("tray accepted an icon above the limit")
	}
	if !proxy.Active() {
		t.Fatal("absence of a watcher discarded the app's icons")
	}
}
