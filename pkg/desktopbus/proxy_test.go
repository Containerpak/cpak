/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
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
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestCanonicalEncodingUsesTheParsedMessage(t *testing.T) {
	raw := &dbus.Message{
		Type: dbus.TypeMethodCall,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/example/Editor")),
			dbus.FieldInterface:   dbus.MakeVariant("org.example.Editor"),
			dbus.FieldMember:      dbus.MakeVariant("Open"),
			dbus.FieldDestination: dbus.MakeVariant("org.freedesktop.systemd1"),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf([]string{})),
		},
		Body: []any{[]string{}},
	}
	encoded, fds, err := encodeMessage(raw, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := dbus.DecodeMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Headers[dbus.FieldDestination] = dbus.MakeVariant("org.example.Editor")
	canonical, err := canonicalPacketData(messagePacket{message: parsed, data: encoded, fds: fds})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbus.DecodeMessage(bytes.NewReader(canonical))
	if err != nil {
		t.Fatal(err)
	}
	if destination, ok := headerValue[string](got, dbus.FieldDestination); !ok || destination != "org.example.Editor" {
		t.Fatalf("canonical destination: %q", destination)
	}
	if len(got.Body) != 1 {
		t.Fatalf("canonical body: %#v", got.Body)
	}
	values, ok := got.Body[0].([]string)
	if !ok || len(values) != 0 {
		t.Fatalf("typed empty array changed: %#v", got.Body[0])
	}
}

func TestSessionBusPolicyAllowsOnlyDeclaredCallsAndNames(t *testing.T) {
	policy := types.DBusPolicy{
		Talk: []types.DBusCallGrant{{
			Name:      "org.example.Editor",
			Path:      "/org/example/Editor",
			Interface: "org.example.Editor.Documents",
			Members:   []string{"Open"},
		}},
		Own: []string{"org.example.Editor.Instance"},
	}
	if !policyBusCallAllowed(policy, "org.example.Editor", "/org/example/Editor", "org.example.Editor.Documents", "Open", nil) {
		t.Fatal("the declared session bus call was refused")
	}
	if policyBusCallAllowed(policy, "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", nil) {
		t.Fatal("an undeclared host execution call was allowed")
	}
	if !policyBusCallAllowed(policy, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RequestName", []any{"org.example.Editor.Instance", uint32(0)}) {
		t.Fatal("the declared own name was refused")
	}
	if policyBusCallAllowed(policy, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RequestName", []any{"org.freedesktop.systemd1", uint32(0)}) {
		t.Fatal("an undeclared own name was allowed")
	}
}

func TestBluetoothBusAllowsOnlyBluezAndRequiredBusCalls(t *testing.T) {
	if !bluetoothBusCallAllowed(bluezDestination, "/org/bluez/hci0", "org.bluez.Adapter1", "StartDiscovery", nil, ":1.9") {
		t.Fatal("a BlueZ call was refused")
	}
	if !bluetoothBusCallAllowed("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "NameHasOwner", []any{bluezDestination}, ":1.9") {
		t.Fatal("the BlueZ owner query was refused")
	}
	if bluetoothBusCallAllowed("org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager", "PowerOff", nil, ":1.9") {
		t.Fatal("an unrelated system service call was allowed")
	}
	if bluetoothBusCallAllowed("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ListNames", nil, ":1.9") {
		t.Fatal("system bus enumeration was allowed")
	}
}

func TestBluetoothProxyRejectsSessionBusPermissions(t *testing.T) {
	err := validateOptions(Options{
		SocketPath:      "/tmp/bluetooth.sock",
		UpstreamAddress: "unix:path=/run/dbus/system_bus_socket",
		Bluetooth:       true,
		Policy: types.DBusPolicy{Talk: []types.DBusCallGrant{{
			Name: "org.example.Service", Path: "/org/example/Service", Interface: "org.example.Service", Members: []string{"Run"},
		}}},
	})
	if err == nil {
		t.Fatal("a Bluetooth proxy accepted a session bus policy")
	}
}

func TestBluetoothSignalsRequireALiveBluezOwner(t *testing.T) {
	proxy := &Proxy{options: Options{Bluetooth: true}}
	message := &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldInterface: dbus.MakeVariant("org.freedesktop.DBus.Properties"),
		},
	}
	if !proxy.interceptBluetooth(nil, nil, message) {
		t.Fatal("a Bluetooth signal became a system bus broadcast without a BlueZ owner")
	}
	proxy.bluezSender = ":1.9"
	if proxy.interceptBluetooth(nil, nil, message) {
		t.Fatal("a Bluetooth signal addressed to the live BlueZ owner was refused")
	}
	if destination, ok := headerValue[string](message, dbus.FieldDestination); !ok || destination != ":1.9" {
		t.Fatalf("Bluetooth signal destination: %q", destination)
	}
}

func TestBluetoothCallbackRepliesReturnToTheirCaller(t *testing.T) {
	proxy := &Proxy{options: Options{Bluetooth: true}, bluezSender: ":1.9"}
	wrong := &connectionState{bluetoothReplies: map[uint32]string{}}
	wrong.allowBluetoothReply(7, ":1.9")
	message := &dbus.Message{
		Type: dbus.TypeMethodReply,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldReplySerial: dbus.MakeVariant(uint32(7)),
			dbus.FieldDestination: dbus.MakeVariant(":1.10"),
		},
	}
	if !proxy.interceptBluetooth(nil, wrong, message) {
		t.Fatal("a BlueZ callback reply was redirected to another system service")
	}
	right := &connectionState{bluetoothReplies: map[uint32]string{}}
	right.allowBluetoothReply(7, ":1.9")
	message.Headers[dbus.FieldDestination] = dbus.MakeVariant(":1.9")
	if proxy.interceptBluetooth(nil, right, message) {
		t.Fatal("a BlueZ callback reply to its caller was refused")
	}
}

func TestBluetoothRepliesAcceptOnlyBluezAndTheBusDaemon(t *testing.T) {
	proxy := &Proxy{options: Options{Bluetooth: true}, bluezSender: ":1.9"}
	state := &connectionState{bluetoothReplies: map[uint32]string{}}
	for sender, allowed := range map[string]bool{
		":1.9":                 true,
		"org.freedesktop.DBus": true,
		":1.10":                false,
	} {
		message := &dbus.Message{
			Type: dbus.TypeMethodReply,
			Headers: map[dbus.HeaderField]dbus.Variant{
				dbus.FieldSender: dbus.MakeVariant(sender),
			},
		}
		if got := proxy.upstreamMessageAllowed(state, message); got != allowed {
			t.Fatalf("reply from %s: got %v, want %v", sender, got, allowed)
		}
	}
}

func TestBluezOwnerWatcherTracksServiceRestarts(t *testing.T) {
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "system.sock")
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

	watcher, signals, owner, err := watchBusNameOwner("unix:path="+upstreamPath, bluezDestination)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if owner != "" {
		t.Fatalf("an absent BlueZ service has owner %s", owner)
	}
	proxy := &Proxy{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go proxy.watchBluezOwner(ctx, signals)

	service, err := dbus.Connect("unix:path=" + upstreamPath)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if reply, requestErr := service.RequestName(bluezDestination, dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request BlueZ name: %d, %v", reply, requestErr)
	}
	var firstOwner string
	if err = service.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, bluezDestination).Store(&firstOwner); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for proxy.currentBluezSender() != firstOwner && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.currentBluezSender() != firstOwner {
		t.Fatalf("BlueZ owner was not recorded: got %s, want %s", proxy.currentBluezSender(), firstOwner)
	}
	if releaseReply, releaseErr := service.ReleaseName(bluezDestination); releaseErr != nil || releaseReply != dbus.ReleaseNameReplyReleased {
		t.Fatalf("release BlueZ name: %d, %v", releaseReply, releaseErr)
	}
	deadline = time.Now().Add(3 * time.Second)
	for proxy.currentBluezSender() != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.currentBluezSender() != "" {
		t.Fatalf("stopped BlueZ service still has owner %s", proxy.currentBluezSender())
	}

	replacement, err := dbus.Connect("unix:path=" + upstreamPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if reply, requestErr := replacement.RequestName(bluezDestination, dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("replace BlueZ name: %d, %v", reply, requestErr)
	}
	var secondOwner string
	if err = replacement.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, bluezDestination).Store(&secondOwner); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for proxy.currentBluezSender() != secondOwner && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if proxy.currentBluezSender() != secondOwner || secondOwner == firstOwner {
		t.Fatalf("replacement BlueZ owner: got %s after %s", proxy.currentBluezSender(), firstOwner)
	}
}

func TestBluezSenderRecoversBeforeTheOwnerWatcher(t *testing.T) {
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "system.sock")
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
	defer service.Close()
	if reply, requestErr := service.RequestName(bluezDestination, dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request BlueZ name: %d, %v", reply, requestErr)
	}
	var owner string
	if err = service.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, bluezDestination).Store(&owner); err != nil {
		t.Fatal(err)
	}
	proxy := &Proxy{options: Options{UpstreamAddress: "unix:path=" + upstreamPath}}
	if !proxy.isBluezSender(owner) {
		t.Fatal("the live BlueZ owner was refused before the watcher caught up")
	}
	if proxy.currentBluezSender() != owner {
		t.Fatalf("recovered owner: got %s, want %s", proxy.currentBluezSender(), owner)
	}
}

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

type bluetoothService struct {
	connection *dbus.Conn
	discovered chan struct{}
}

func (s bluetoothService) StartDiscovery() *dbus.Error {
	s.discovered <- struct{}{}
	return nil
}

func (s bluetoothService) Confirm(sender dbus.Sender, agent dbus.ObjectPath) *dbus.Error {
	call := s.connection.Object(string(sender), agent).Call("org.bluez.Agent1.RequestConfirmation", 0, dbus.ObjectPath("/org/bluez/hci0/dev_00_11_22_33_44_55"), uint32(123456))
	if call.Err != nil {
		return dbus.MakeFailedError(call.Err)
	}
	return nil
}

func (s bluetoothService) ConnectProfile(sender dbus.Sender, profile dbus.ObjectPath) *dbus.Error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	defer reader.Close()
	call := s.connection.Object(string(sender), profile).Call(
		"org.bluez.Profile1.NewConnection",
		0,
		dbus.ObjectPath("/org/bluez/hci0/dev_00_11_22_33_44_55"),
		dbus.UnixFD(writer.Fd()),
		map[string]dbus.Variant{},
	)
	writer.Close()
	if call.Err != nil {
		return dbus.MakeFailedError(call.Err)
	}
	content := make([]byte, 1)
	if _, err = reader.Read(content); err != nil {
		return dbus.MakeFailedError(err)
	}
	if content[0] != 0x42 {
		return dbus.MakeFailedError(errors.New("profile sent an unexpected byte"))
	}
	return nil
}

type bluetoothAgent struct {
	confirmed chan uint32
}

func (a bluetoothAgent) RequestConfirmation(_ dbus.ObjectPath, passkey uint32) *dbus.Error {
	a.confirmed <- passkey
	return nil
}

type bluetoothProfile struct {
	connected chan struct{}
}

func (p bluetoothProfile) NewConnection(_ dbus.ObjectPath, descriptor dbus.UnixFD, _ map[string]dbus.Variant) *dbus.Error {
	connection := os.NewFile(uintptr(descriptor), "BlueZ profile connection")
	if connection == nil {
		return dbus.MakeFailedError(errors.New("profile connection descriptor is invalid"))
	}
	defer connection.Close()
	if _, err := connection.Write([]byte{0x42}); err != nil {
		return dbus.MakeFailedError(err)
	}
	p.connected <- struct{}{}
	return nil
}

func TestBluetoothProxyForwardsBluezOnly(t *testing.T) {
	directory := t.TempDir()
	upstreamPath := filepath.Join(directory, "system.sock")
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
	if reply, requestErr := service.RequestName(bluezDestination, dbus.NameFlagDoNotQueue); requestErr != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request BlueZ name: %d, %v", reply, requestErr)
	}
	discovered := make(chan struct{}, 1)
	if err = service.Export(bluetoothService{connection: service, discovered: discovered}, "/org/bluez/hci0", "org.bluez.Adapter1"); err != nil {
		t.Fatal(err)
	}

	proxyPath := filepath.Join(directory, "proxy.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serveError := make(chan error, 1)
	go func() {
		serveError <- Serve(ctx, Options{
			SocketPath:      proxyPath,
			UpstreamAddress: "unix:path=" + upstreamPath,
			Bluetooth:       true,
		})
	}()
	waitForPath(t, proxyPath)

	connection, err := dbus.Connect("unix:path=" + proxyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err = connection.Object(bluezDestination, "/org/bluez/hci0").Call("org.bluez.Adapter1.StartDiscovery", 0).Err; err != nil {
		t.Fatalf("call BlueZ: %v", err)
	}
	select {
	case <-discovered:
	case <-time.After(3 * time.Second):
		t.Fatal("the BlueZ method did not reach the service")
	}
	if err = connection.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Err; err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("system bus enumeration was not denied: %v", err)
	}
	if err = connection.Object("org.freedesktop.login1", "/org/freedesktop/login1").Call("org.freedesktop.login1.Manager.PowerOff", 0, false).Err; err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("unrelated system service was not denied: %v", err)
	}

	signals := make(chan *dbus.Signal, 1)
	connection.Signal(signals)
	defer connection.RemoveSignal(signals)
	if err = connection.AddMatchSignal(dbus.WithMatchInterface("org.freedesktop.DBus.Properties"), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		t.Fatal(err)
	}
	if err = service.Emit("/org/bluez/hci0", "org.freedesktop.DBus.Properties.PropertiesChanged", "org.bluez.Adapter1", map[string]dbus.Variant{"Discovering": dbus.MakeVariant(true)}, []string{}); err != nil {
		t.Fatal(err)
	}
	select {
	case signal := <-signals:
		if signal.Name != "org.freedesktop.DBus.Properties.PropertiesChanged" {
			t.Fatalf("unexpected BlueZ signal: %s", signal.Name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the BlueZ signal did not reach the client")
	}

	confirmed := make(chan uint32, 1)
	agentPath := dbus.ObjectPath("/org/bluez/cpak_agent")
	if err = connection.Export(bluetoothAgent{confirmed: confirmed}, agentPath, "org.bluez.Agent1"); err != nil {
		t.Fatal(err)
	}
	if err = connection.Object(bluezDestination, "/org/bluez/hci0").Call("org.bluez.Adapter1.Confirm", 0, agentPath).Err; err != nil {
		t.Fatalf("BlueZ callback: %v", err)
	}
	select {
	case passkey := <-confirmed:
		if passkey != 123456 {
			t.Fatalf("unexpected passkey: %d", passkey)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the BlueZ callback did not reach the exported agent")
	}

	connected := make(chan struct{}, 1)
	profilePath := dbus.ObjectPath("/org/bluez/cpak_profile")
	if err = connection.Export(bluetoothProfile{connected: connected}, profilePath, "org.bluez.Profile1"); err != nil {
		t.Fatal(err)
	}
	if err = connection.Object(bluezDestination, "/org/bluez/hci0").Call("org.bluez.Adapter1.ConnectProfile", 0, profilePath).Err; err != nil {
		t.Fatalf("BlueZ profile callback: %v", err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("the BlueZ profile descriptor did not reach the client")
	}

	cancel()
	select {
	case err = <-serveError:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Bluetooth proxy did not stop")
	}
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
			FilePicker:      true,
			Policy: types.DBusPolicy{Talk: []types.DBusCallGrant{
				{Name: "org.example.CpakDesktopBus", Path: "/org/example/CpakDesktopBus", Interface: "org.example.CpakDesktopBus", Members: []string{"StartTransientUnit"}},
				{Name: "org.freedesktop.DBus", Path: "/org/freedesktop/DBus", Interface: "org.freedesktop.DBus", Members: []string{"ListNames"}},
			}},
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
			FilePicker:      true,
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

// With the session bus closed, the default-deny used to cover method calls and
// nothing else, so a confined application's signals, replies and errors were
// forwarded to the real bus with its unique name as sender. Every type it can
// emit is held to the same policy.
func TestAClientCannotEmitSignalsRepliesOrErrors(t *testing.T) {
	proxy := &Proxy{}
	for _, kind := range []dbus.Type{dbus.TypeSignal, dbus.TypeMethodReply, dbus.TypeError} {
		message := &dbus.Message{Type: kind}
		if !proxy.intercept(context.Background(), nil, nil, message) {
			t.Fatalf("a %v from a confined client was forwarded to the session bus", kind)
		}
	}
}
