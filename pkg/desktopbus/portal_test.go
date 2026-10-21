/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/runtimeproto"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
)

type portalHarness struct {
	connection *dbus.Conn
	opened     chan systembroker.OpenURIRequest
	notified   chan systembroker.NotificationRequest
}

// startPortalBus serves a portal bus with no host bus anywhere: the upstream
// address is left empty, so a proxy that still tried to reach one would fail
// before a single call is answered.
func startPortalBus(t *testing.T, options Options) portalHarness {
	t.Helper()
	directory := t.TempDir()
	harness := portalHarness{
		opened:   make(chan systembroker.OpenURIRequest, 1),
		notified: make(chan systembroker.NotificationRequest, 1),
	}
	options.SocketPath = filepath.Join(directory, "bus.sock")
	options.Portals = true
	options.OpenURIHandler = func(_ context.Context, request systembroker.OpenURIRequest) error {
		harness.opened <- request
		return nil
	}
	options.Notify = func(_ context.Context, request systembroker.NotificationRequest) error {
		harness.notified <- request
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, options) }()
	waitForPath(t, options.SocketPath)
	connection, err := dbus.Connect("unix:path=" + options.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	harness.connection = connection
	return harness
}

func portal(connection *dbus.Conn) dbus.BusObject {
	return connection.Object(portalDestination, portalObjectPath)
}

func TestPortalBusAnswersWithoutAHostBus(t *testing.T) {
	harness := startPortalBus(t, Options{ColorScheme: 1, IconTheme: "Adwaita"})
	connection := harness.connection
	if !strings.HasPrefix(connection.Names()[0], ":1.") {
		t.Fatalf("the bus gave the client the name %v", connection.Names())
	}
	var owner string
	if err := connection.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, portalDestination).Store(&owner); err != nil || owner != portalUniqueName {
		t.Fatalf("the portal is owned by %q: %v", owner, err)
	}
	var version dbus.Variant
	if err := portal(connection).Call(propertiesInterface+".Get", 0, settingsInterface, "version").Store(&version); err != nil {
		t.Fatal(err)
	}
	var scheme dbus.Variant
	if err := portal(connection).Call(settingsInterface+".ReadOne", 0, appearanceNamespace, "color-scheme").Store(&scheme); err != nil || scheme.Value() != uint32(1) {
		t.Fatalf("the colour scheme read as %v: %v", scheme, err)
	}
	var all map[string]map[string]dbus.Variant
	if err := portal(connection).Call(settingsInterface+".ReadAll", 0, []string{"org.gnome.desktop.*"}).Store(&all); err != nil {
		t.Fatal(err)
	}
	if all[interfaceNamespace]["icon-theme"].Value() != "Adwaita" || all[appearanceNamespace] != nil {
		t.Fatalf("ReadAll answered %v", all)
	}
	var proxies []string
	if err := portal(connection).Call(proxyInterface+".Lookup", 0, "https://example.org").Store(&proxies); err != nil || len(proxies) != 1 || proxies[0] != "direct://" {
		t.Fatalf("the proxy resolver answered %v: %v", proxies, err)
	}
	var available bool
	if err := portal(connection).Call(networkInterface+".GetAvailable", 0).Store(&available); err != nil || available {
		t.Fatalf("an application without network was told the network is available: %v", err)
	}
}

func TestPortalBusKeepsEverythingElseClosed(t *testing.T) {
	harness := startPortalBus(t, Options{Policy: types.DBusPolicy{Own: []string{"org.example.App"}}})
	connection := harness.connection
	err := connection.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").Call("org.freedesktop.systemd1.Manager.StartTransientUnit", 0).Err
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("a host service was reachable: %v", err)
	}
	if err := portal(connection).Call("org.freedesktop.portal.Screenshot.Screenshot", 0, "", map[string]dbus.Variant{}).Err; err == nil {
		t.Fatal("a portal cpak does not provide answered")
	}
	if err := connection.Object(notificationsName, notificationsPath).Call(notificationsName+".GetCapabilities", 0).Err; err == nil {
		t.Fatal("notifications answered without the permission")
	}
	if _, err := connection.RequestName("org.example.Other", dbus.NameFlagDoNotQueue); err == nil {
		t.Fatal("a name outside the policy was granted")
	}
	if reply, err := connection.RequestName("org.example.App", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("a name the policy allows was refused: %v", err)
	}
	if err := connection.Object(flatpakPortalName, flatpakPortalPath).Call(flatpakPortalInterface+".Spawn", 0).Err; err == nil {
		t.Fatal("processes could be started without a container to start them in")
	}
}

func TestPortalOpensLinksAndNotifiesThroughTheBroker(t *testing.T) {
	harness := startPortalBus(t, Options{OpenURI: true, Notifications: true, AppName: "org.example.App"})
	connection := harness.connection
	signals := make(chan *dbus.Signal, 4)
	connection.Signal(signals)
	if err := connection.AddMatchSignal(dbus.WithMatchInterface(requestInterface)); err != nil {
		t.Fatal(err)
	}
	var handle dbus.ObjectPath
	options := map[string]dbus.Variant{"handle_token": dbus.MakeVariant("link1")}
	if err := portal(connection).Call(openURIInterface+".OpenURI", 0, "", "https://example.org/", options).Store(&handle); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-harness.opened:
		if request.URI != "https://example.org/" {
			t.Fatalf("the broker was asked to open %s", request.URI)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the link never reached the broker")
	}
	select {
	case signal := <-signals:
		if signal.Path != handle || signal.Body[0] != uint32(0) {
			t.Fatalf("the request answered %v on %s", signal.Body, signal.Path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the request never answered")
	}

	notification := map[string]dbus.Variant{"title": dbus.MakeVariant("Done"), "body": dbus.MakeVariant("It worked")}
	if err := portal(connection).Call(notificationPortal+".AddNotification", 0, "id", notification).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-harness.notified:
		if request.Summary != "Done" || request.Body != "It worked" || request.AppName != "org.example.App" {
			t.Fatalf("the broker was asked to show %+v", request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the notification never reached the broker")
	}
}

// fakeRuntime answers the container command socket the way the runtime does
// for a command started on descriptors: it reports the descriptors it was
// handed by writing to the one placed at 1, then exits with a known code.
func fakeRuntime(t *testing.T, path string, received chan runtimeproto.Request) {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		connection, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer connection.Close()
		_, payload, files, err := runtimeproto.ReadRequest(connection)
		if err != nil {
			return
		}
		request, _ := runtimeproto.DecodeRequest(payload)
		for index, target := range request.Descriptors {
			if target == 1 {
				files[index].Write([]byte("spawned\n"))
			}
			files[index].Close()
		}
		received <- request
		_ = runtimeproto.NewWriter(connection).Write(runtimeproto.FrameExit, runtimeproto.EncodeExit(7))
	}()
}

func TestFlatpakSpawnStartsTheCommandInTheContainer(t *testing.T) {
	execSocket := filepath.Join(t.TempDir(), "exec.sock")
	received := make(chan runtimeproto.Request, 1)
	fakeRuntime(t, execSocket, received)
	harness := startPortalBus(t, Options{ExecSocketPath: execSocket})
	connection := harness.connection
	signals := make(chan *dbus.Signal, 4)
	connection.Signal(signals)
	if err := connection.AddMatchSignal(dbus.WithMatchInterface(flatpakPortalInterface)); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	fds := map[uint32]dbus.UnixFD{1: dbus.UnixFD(writer.Fd())}
	argv := [][]byte{append([]byte("/usr/bin/glycin-svg"), 0)}
	env := map[string]string{"GLYCIN": "1", "LD_PRELOAD": "/evil.so"}
	var pid uint32
	err = connection.Object(flatpakPortalName, flatpakPortalPath).Call(flatpakPortalInterface+".Spawn", 0,
		append([]byte("/app"), 0), argv, fds, env, uint32(spawnFlagWatchBus), map[string]dbus.Variant{}).Store(&pid)
	writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-received:
		if strings.Join(request.Args, " ") != "/usr/bin/glycin-svg" || request.Directory != "/app" {
			t.Fatalf("the container was asked to run %+v", request)
		}
		if len(request.Descriptors) != 1 || request.Descriptors[0] != 1 {
			t.Fatalf("the descriptors were placed at %v", request.Descriptors)
		}
		if strings.Join(request.Env, " ") != "GLYCIN=1" {
			t.Fatalf("the environment passed was %v", request.Env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the container was never asked to start the command")
	}
	output := make([]byte, 8)
	if _, err := reader.Read(output); err != nil || string(output) != "spawned\n" {
		t.Fatalf("the descriptor handed over was not the caller's: %q %v", output, err)
	}
	select {
	case signal := <-signals:
		if signal.Name != flatpakPortalInterface+".SpawnExited" || signal.Body[0] != pid || signal.Body[1] != uint32(7<<8) {
			t.Fatalf("the caller was told %s %v", signal.Name, signal.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the caller was never told the process exited")
	}
}
