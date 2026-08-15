/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"context"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
)

type transientProperty struct {
	Name  string
	Value dbus.Variant
}

type transientAuxiliary struct {
	Name       string
	Properties []transientProperty
}

type transientService struct {
	called chan struct{}
}

func (s transientService) StartTransientUnit(_ string, _ string, _ []transientProperty, _ []transientAuxiliary) *dbus.Error {
	s.called <- struct{}{}
	return nil
}

func TestProxyAcceptsExternalAuthenticationWithoutInitialResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	authenticated := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			authenticated <- acceptErr
			return
		}
		defer connection.Close()
		authenticated <- authenticateClient(connection)
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	if err = writeAuthLine(client, "AUTH EXTERNAL"); err != nil {
		t.Fatal(err)
	}
	if line, readErr := readAuthLine(client); readErr != nil || line != "DATA" {
		t.Fatalf("challenge: got %q, %v", line, readErr)
	}
	identity := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if err = writeAuthLine(client, "DATA "+identity); err != nil {
		t.Fatal(err)
	}
	if line, readErr := readAuthLine(client); readErr != nil || !strings.HasPrefix(line, "OK ") {
		t.Fatalf("authentication: got %q, %v", line, readErr)
	}
	if err = writeAuthLine(client, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	if err = <-authenticated; err != nil {
		t.Fatal(err)
	}
}

func TestResponseSignalTargetsPortalCaller(t *testing.T) {
	values := map[string]dbus.Variant{
		"uris": dbus.MakeVariant([]string{"file:///run/cpak/grants/example/document.pdf"}),
	}
	message := responseSignal(
		dbus.ObjectPath("/org/freedesktop/portal/desktop/request/1_42/chrome_chooser"),
		":1.7",
		":1.42",
		0,
		values,
	)
	if sender, ok := headerValue[string](message, dbus.FieldSender); !ok || sender != ":1.7" {
		t.Fatalf("response sender: %q", sender)
	}
	if destination, ok := headerValue[string](message, dbus.FieldDestination); !ok || destination != ":1.42" {
		t.Fatalf("response destination: %q", destination)
	}
	if signature, ok := headerValue[dbus.Signature](message, dbus.FieldSignature); !ok || signature.String() != "ua{sv}" {
		t.Fatalf("response signature: %q", signature)
	}
}

func TestProxyForwardsBusAndInterceptsFileChooser(t *testing.T) {
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "upstream.sock")
	upstream := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=unix:path="+upstreamPath)
	upstream.Stdout = os.Stderr
	upstream.Stderr = os.Stderr
	if err := upstream.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = upstream.Process.Kill()
		_ = upstream.Wait()
	})
	waitForPath(t, upstreamPath)
	service, err := dbus.Connect("unix:path=" + upstreamPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	if reply, requestErr := service.RequestName("org.example.CpakDesktopBus", dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request service name: %d, %v", reply, requestErr)
	}
	if reply, requestErr := service.RequestName(portalDestination, dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request portal name: %d, %v", reply, requestErr)
	}
	transientCalled := make(chan struct{}, 1)
	if err = service.Export(transientService{called: transientCalled}, "/org/example/CpakDesktopBus", "org.example.CpakDesktopBus"); err != nil {
		t.Fatal(err)
	}

	proxyPath := filepath.Join(directory, "proxy.sock")
	selected := make(chan systembroker.FilePickerRequest, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serveError := make(chan error, 1)
	go func() {
		serveError <- Serve(ctx, Options{
			SocketPath:      proxyPath,
			UpstreamAddress: "unix:path=" + upstreamPath,
			AllowSessionBus: true,
			PickFile: func(_ context.Context, request systembroker.FilePickerRequest) (systembroker.FilePickerResult, error) {
				selected <- request
				return systembroker.FilePickerResult{
					Path:     "/run/cpak/grants/example/game.exe",
					Paths:    []string{"/run/cpak/grants/example/game.exe"},
					Kind:     "file",
					Access:   "read-only",
					Lifetime: "session",
				}, nil
			},
		})
	}()
	waitForPath(t, proxyPath)

	connection, err := dbus.Connect("unix:path=" + proxyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	properties := []transientProperty{{Name: "PIDs", Value: dbus.MakeVariant([]uint32{uint32(os.Getpid())})}}
	if err = connection.Object("org.example.CpakDesktopBus", "/org/example/CpakDesktopBus").Call("org.example.CpakDesktopBus.StartTransientUnit", 0, "app-test.scope", "replace", properties, []transientAuxiliary{}).Err; err != nil {
		t.Fatalf("forward empty typed array: %v", err)
	}
	select {
	case <-transientCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("typed empty array did not reach the upstream service")
	}
	var names []string
	if err = connection.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names); err != nil {
		t.Fatalf("forward ordinary bus method: %v", err)
	}
	signals := make(chan *dbus.Signal, 1)
	connection.Signal(signals)
	defer connection.RemoveSignal(signals)
	if err = connection.AddMatchSignal(dbus.WithMatchInterface(requestInterface), dbus.WithMatchMember("Response")); err != nil {
		t.Fatal(err)
	}
	options := map[string]dbus.Variant{
		"multiple":       dbus.MakeVariant(true),
		"directory":      dbus.MakeVariant(false),
		"handle_token":   dbus.MakeVariant("chrome_chooser"),
		"current_folder": dbus.MakeVariant(append([]byte("/home/test/Downloads"), 0)),
	}
	var handle dbus.ObjectPath
	if err = connection.Object(portalDestination, portalObjectPath).Call(fileChooserInterface+".OpenFile", 0, "", "Select executable", options).Store(&handle); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(handle), "/chrome_chooser") {
		t.Fatalf("request handle does not preserve the portal token: %s", handle)
	}
	select {
	case request := <-selected:
		if request.Title != "Select executable" || !request.Multiple || request.CurrentFolder != "/home/test/Downloads" {
			t.Fatalf("unexpected picker request: %+v", request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("file chooser request was not intercepted")
	}
	select {
	case signal := <-signals:
		if signal.Path != handle || signal.Name != requestInterface+".Response" {
			t.Fatalf("unexpected response signal: %+v", signal)
		}
		var portalOwner string
		if err = connection.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, portalDestination).Store(&portalOwner); err != nil {
			t.Fatal(err)
		}
		if signal.Sender != portalOwner {
			t.Fatalf("response sender: %s, expected %s", signal.Sender, portalOwner)
		}
		response := signal.Body[0].(uint32)
		values := signal.Body[1].(map[string]dbus.Variant)
		uris := values["uris"].Value().([]string)
		if response != 0 || len(uris) != 1 || uris[0] != "file:///run/cpak/grants/example/game.exe" {
			t.Fatalf("unexpected response: %d %+v", response, values)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("file chooser response was not delivered")
	}
	var portalOwner string
	if err = connection.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, portalDestination).Store(&portalOwner); err != nil {
		t.Fatal(err)
	}
	options["handle_token"] = dbus.MakeVariant("chrome_direct_chooser")
	if err = connection.Object(portalOwner, portalObjectPath).Call(fileChooserInterface+".OpenFile", 0, "", "Select executable", options).Store(&handle); err != nil {
		t.Fatal(err)
	}
	select {
	case <-selected:
	case <-time.After(3 * time.Second):
		t.Fatal("file chooser request addressed to the portal owner was not intercepted")
	}
	select {
	case signal := <-signals:
		if signal.Path != handle || signal.Name != requestInterface+".Response" {
			t.Fatalf("unexpected direct response signal: %+v", signal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("direct file chooser response was not delivered")
	}
	testGIOPortalResponse(t, proxyPath)
	cancel()
	select {
	case err = <-serveError:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("desktop bus proxy did not stop")
	}
}

func testGIOPortalResponse(t *testing.T, socketPath string) {
	t.Helper()
	if err := exec.Command("python3", "-c", "import gi").Run(); err != nil {
		t.Log("Python GIO bindings are unavailable")
		return
	}
	program := `
import sys
from gi.repository import Gio, GLib

address = "unix:path=" + sys.argv[1]
flags = Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION
connection = Gio.DBusConnection.new_for_address_sync(address, flags, None, None)
sender = connection.get_unique_name()[1:].replace(".", "_")
token = "gio_chooser"
path = "/org/freedesktop/portal/desktop/request/" + sender + "/" + token
loop = GLib.MainLoop()
received = []

def response(connection, sender, path, interface, member, parameters):
    received.append(parameters.unpack())
    loop.quit()

connection.signal_subscribe(
    "org.freedesktop.portal.Desktop",
    "org.freedesktop.portal.Request",
    "Response",
    path,
    None,
    Gio.DBusSignalFlags.NONE,
    response,
)
connection.call_sync(
    "org.freedesktop.DBus",
    "/org/freedesktop/DBus",
    "org.freedesktop.DBus",
    "ListNames",
    None,
    GLib.VariantType("(as)"),
    Gio.DBusCallFlags.NONE,
    5000,
    None,
)
options = {
    "handle_token": GLib.Variant("s", token),
    "multiple": GLib.Variant("b", False),
}
reply = connection.call_sync(
    "org.freedesktop.portal.Desktop",
    "/org/freedesktop/portal/desktop",
    "org.freedesktop.portal.FileChooser",
    "OpenFile",
    GLib.Variant("(ssa{sv})", ("", "Select file", options)),
    GLib.VariantType("(o)"),
    Gio.DBusCallFlags.NONE,
    5000,
    None,
)
if reply.unpack()[0] != path:
    raise RuntimeError("unexpected request path: " + reply.unpack()[0])
GLib.timeout_add(5000, lambda: loop.quit() or False)
loop.run()
if not received:
    raise RuntimeError("GIO did not receive the response signal")
response_code, values = received[0]
if response_code != 0 or values.get("uris") != ["file:///run/cpak/grants/example/game.exe"]:
    raise RuntimeError("unexpected response: " + repr(received[0]))
`
	command := exec.Command("python3", "-c", program, socketPath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("GIO portal client: %v\n%s", err, output)
	}
}

func TestProxyRestrictsSessionBusWithoutPermission(t *testing.T) {
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "upstream.sock")
	upstream := exec.Command("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=unix:path="+upstreamPath)
	upstream.Stdout = os.Stderr
	upstream.Stderr = os.Stderr
	if err := upstream.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = upstream.Process.Kill()
		_ = upstream.Wait()
	})
	waitForPath(t, upstreamPath)

	proxyPath := filepath.Join(directory, "proxy.sock")
	selected := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = Serve(ctx, Options{
			SocketPath:      proxyPath,
			UpstreamAddress: "unix:path=" + upstreamPath,
			PickFile: func(_ context.Context, _ systembroker.FilePickerRequest) (systembroker.FilePickerResult, error) {
				selected <- struct{}{}
				return systembroker.FilePickerResult{Path: "/run/cpak/grants/example/document.pdf", Paths: []string{"/run/cpak/grants/example/document.pdf"}}, nil
			},
		})
	}()
	waitForPath(t, proxyPath)

	connection, err := dbus.Connect("unix:path=" + proxyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Err; err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("unrestricted session bus call: %v", err)
	}
	options := map[string]dbus.Variant{"handle_token": dbus.MakeVariant("restricted_picker")}
	var handle dbus.ObjectPath
	if err = connection.Object(portalDestination, portalObjectPath).Call(fileChooserInterface+".OpenFile", 0, "", "Select file", options).Store(&handle); err != nil {
		t.Fatal(err)
	}
	select {
	case <-selected:
	case <-time.After(3 * time.Second):
		t.Fatal("restricted file chooser request was not intercepted")
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("path did not appear: %s", path)
}
