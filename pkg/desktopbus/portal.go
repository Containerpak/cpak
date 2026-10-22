/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
)

// In portal mode the proxy is the whole session bus of the sandbox. Nothing is
// forwarded to the host bus, which does not even have to exist: the bus
// daemon's own methods are answered here, the desktop portal is answered here,
// and what a portal call needs from the host is asked of the cpak system
// broker over its own socket, which applies the application's policy.
//
// Toolkits inside a Flatpak runtime reach the desktop only through portals, so
// this is enough for them to open files, open links, notify, follow the colour
// scheme and see the network, and nothing else of the desktop is reachable.

const (
	busName             = "org.freedesktop.DBus"
	busPath             = dbus.ObjectPath("/org/freedesktop/DBus")
	notificationsName   = "org.freedesktop.Notifications"
	notificationsPath   = dbus.ObjectPath("/org/freedesktop/Notifications")
	openURIInterface    = "org.freedesktop.portal.OpenURI"
	notificationPortal  = "org.freedesktop.portal.Notification"
	proxyInterface      = "org.freedesktop.portal.ProxyResolver"
	propertiesInterface = "org.freedesktop.DBus.Properties"
	introspectInterface = "org.freedesktop.DBus.Introspectable"
	peerInterface       = "org.freedesktop.DBus.Peer"
	portalUniqueName    = ":1.0"
	appearanceNamespace = "org.freedesktop.appearance"
	interfaceNamespace  = "org.gnome.desktop.interface"

	errorUnknownMethod  = "org.freedesktop.DBus.Error.UnknownMethod"
	errorServiceUnknown = "org.freedesktop.DBus.Error.ServiceUnknown"
	errorNameHasNoOwner = "org.freedesktop.DBus.Error.NameHasNoOwner"
	errorAccessDenied   = "org.freedesktop.DBus.Error.AccessDenied"
	errorInvalidArgs    = "org.freedesktop.DBus.Error.InvalidArgs"
	errorPortalNotFound = "org.freedesktop.portal.Error.NotFound"
	errorNotAllowed     = "org.freedesktop.portal.Error.NotAllowed"
)

// portalVersions are the interfaces cpak answers and the version of each it
// speaks. Toolkits read the version property before choosing which calls to
// make, so it is what keeps them to the calls implemented here.
var portalVersions = map[string]uint32{
	fileChooserInterface: 3,
	openURIInterface:     3,
	settingsInterface:    2,
	networkInterface:     3,
	notificationPortal:   1,
	proxyInterface:       1,
}

type portalBus struct {
	proxy    *Proxy
	busID    string
	nextName atomic.Uint32
	notifyID atomic.Uint32
	nextPID  atomic.Uint32
}

func newPortalBus(proxy *Proxy) *portalBus {
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &portalBus{proxy: proxy, busID: hex.EncodeToString(id)}
}

type portalConnection struct {
	mu      sync.Mutex
	unique  string
	owned   map[string]bool
	spawned map[uint32]*spawned
}

func (c *portalConnection) name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unique
}

func (b *portalBus) serveConnection(parent context.Context, raw *net.UnixConn) {
	defer raw.Close()
	if err := authenticateClient(raw); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = raw.Close()
	}()
	client := &serializedConn{UnixConn: raw}
	client.serial.Store(1 << 30)
	connection := &portalConnection{owned: map[string]bool{}}
	defer connection.endSpawned()
	for {
		packet, err := readMessage(raw)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && ctx.Err() == nil {
				logger.Printf("Desktop bus connection closed: %v", err)
			}
			return
		}
		message := packet.message
		if len(packet.fds) > 0 {
			// godbus turns a descriptor into a value of a different type than
			// the one it builds a{uh} maps with, and loses the rest of the body
			// to it. Read the call again without them: every descriptor is then
			// an index into packet.fds, which is what the portal needs.
			if indexed, err := dbus.DecodeMessage(bytes.NewReader(packet.data)); err == nil {
				message = indexed
			}
		}
		if message.Type == dbus.TypeMethodCall {
			b.dispatch(ctx, client, connection, message, packet.fds)
		}
		packet.closeFDs()
	}
}

func (b *portalBus) reply(client *serializedConn, connection *portalConnection, call *dbus.Message, sender string, body ...any) {
	message := methodReply(call, body)
	message.Headers[dbus.FieldSender] = dbus.MakeVariant(sender)
	if name := connection.name(); name != "" {
		message.Headers[dbus.FieldDestination] = dbus.MakeVariant(name)
	}
	_ = client.writeSyntheticMessage(message)
}

func (b *portalBus) fail(client *serializedConn, connection *portalConnection, call *dbus.Message, sender, name, detail string) {
	message := methodError(call, name, detail)
	message.Headers[dbus.FieldSender] = dbus.MakeVariant(sender)
	if unique := connection.name(); unique != "" {
		message.Headers[dbus.FieldDestination] = dbus.MakeVariant(unique)
	}
	_ = client.writeSyntheticMessage(message)
}

func (b *portalBus) dispatch(ctx context.Context, client *serializedConn, connection *portalConnection, call *dbus.Message, fds []int) {
	path, _ := headerValue[dbus.ObjectPath](call, dbus.FieldPath)
	interfaceName, _ := headerValue[string](call, dbus.FieldInterface)
	member, _ := headerValue[string](call, dbus.FieldMember)
	destination, _ := headerValue[string](call, dbus.FieldDestination)
	switch {
	case destination == busName || destination == "" && path == busPath:
		b.busMethod(client, connection, call, interfaceName, member)
	case destination == portalUniqueName && path == flatpakPortalPath && b.proxy.options.ExecSocketPath != "":
		b.flatpakMethod(client, connection, call, fds, path, interfaceName, member)
	case destination == portalUniqueName && path == notificationsPath && b.proxy.options.Notifications:
		b.notificationsMethod(ctx, client, connection, call, path, interfaceName, member)
	case b.isPortal(destination):
		b.portalMethod(ctx, client, connection, call, path, interfaceName, member)
	case destination == flatpakPortalName:
		b.flatpakMethod(client, connection, call, fds, path, interfaceName, member)
	case destination == notificationsName && b.proxy.options.Notifications:
		b.notificationsMethod(ctx, client, connection, call, path, interfaceName, member)
	default:
		b.fail(client, connection, call, busName, errorServiceUnknown, "service is not available in this cpak")
	}
}

func (b *portalBus) isPortal(destination string) bool {
	return destination == portalDestination || destination == portalUniqueName
}

// owner answers who owns a name on this bus. The portal and the notification
// service are owned by the proxy itself, and a client owns its unique name and
// whatever its policy let it request.
func (b *portalBus) owner(connection *portalConnection, name string) (string, bool) {
	switch {
	case name == busName:
		return busName, true
	case name == portalDestination || name == portalUniqueName:
		return portalUniqueName, true
	case name == notificationsName && b.proxy.options.Notifications:
		return portalUniqueName, true
	case name == flatpakPortalName && b.proxy.options.ExecSocketPath != "":
		return portalUniqueName, true
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if name == connection.unique || connection.owned[name] {
		return connection.unique, connection.unique != ""
	}
	return "", false
}

func (b *portalBus) busMethod(client *serializedConn, connection *portalConnection, call *dbus.Message, interfaceName, member string) {
	if interfaceName == peerInterface && member == "Ping" {
		b.reply(client, connection, call, busName)
		return
	}
	if interfaceName != busName && interfaceName != "" {
		b.fail(client, connection, call, busName, errorUnknownMethod, "unknown interface")
		return
	}
	body := call.Body
	switch member {
	case "Hello":
		connection.mu.Lock()
		if connection.unique != "" {
			connection.mu.Unlock()
			b.fail(client, connection, call, busName, "org.freedesktop.DBus.Error.Failed", "already connected")
			return
		}
		connection.unique = fmt.Sprintf(":1.%d", b.nextName.Add(1))
		unique := connection.unique
		connection.mu.Unlock()
		b.reply(client, connection, call, busName, unique)
		b.nameSignal(client, unique, "NameAcquired", unique)
	case "AddMatch", "RemoveMatch":
		b.reply(client, connection, call, busName)
	case "GetId":
		b.reply(client, connection, call, busName, b.busID)
	case "GetNameOwner", "NameHasOwner":
		name, ok := singleString(body)
		if !ok {
			b.fail(client, connection, call, busName, errorInvalidArgs, "expected a name")
			return
		}
		owner, owned := b.owner(connection, name)
		if member == "NameHasOwner" {
			b.reply(client, connection, call, busName, owned)
			return
		}
		if !owned {
			b.fail(client, connection, call, busName, errorNameHasNoOwner, "the name has no owner")
			return
		}
		b.reply(client, connection, call, busName, owner)
	case "ListNames", "ListActivatableNames":
		names := []string{busName, portalDestination}
		if b.proxy.options.ExecSocketPath != "" {
			names = append(names, flatpakPortalName)
		}
		if b.proxy.options.Notifications {
			names = append(names, notificationsName)
		}
		if member == "ListNames" {
			names = append(names, portalUniqueName)
			if unique := connection.name(); unique != "" {
				names = append(names, unique)
			}
		}
		b.reply(client, connection, call, busName, names)
	case "StartServiceByName":
		if len(body) != 2 {
			b.fail(client, connection, call, busName, errorInvalidArgs, "expected a name and flags")
			return
		}
		name, _ := body[0].(string)
		if _, owned := b.owner(connection, name); !owned || strings.HasPrefix(name, ":") {
			b.fail(client, connection, call, busName, errorServiceUnknown, "service is not available in this cpak")
			return
		}
		b.reply(client, connection, call, busName, uint32(2))
	case "RequestName":
		if len(body) != 2 {
			b.fail(client, connection, call, busName, errorInvalidArgs, "expected a name and flags")
			return
		}
		name, _ := body[0].(string)
		if !b.proxy.options.Policy.AllowsOwn(name) {
			b.fail(client, connection, call, busName, errorAccessDenied, "owning that name is not permitted")
			return
		}
		connection.mu.Lock()
		already := connection.owned[name]
		connection.owned[name] = true
		unique := connection.unique
		connection.mu.Unlock()
		if already {
			b.reply(client, connection, call, busName, uint32(4))
			return
		}
		b.reply(client, connection, call, busName, uint32(1))
		b.nameSignal(client, unique, "NameAcquired", name)
	case "ReleaseName":
		name, ok := singleString(body)
		if !ok {
			b.fail(client, connection, call, busName, errorInvalidArgs, "expected a name")
			return
		}
		connection.mu.Lock()
		owned := connection.owned[name]
		delete(connection.owned, name)
		unique := connection.unique
		connection.mu.Unlock()
		if !owned {
			b.reply(client, connection, call, busName, uint32(3))
			return
		}
		b.reply(client, connection, call, busName, uint32(1))
		b.nameSignal(client, unique, "NameLost", name)
	default:
		b.fail(client, connection, call, busName, errorAccessDenied, "session bus access is not permitted")
	}
}

func (b *portalBus) nameSignal(client *serializedConn, destination, member, name string) {
	_ = client.writeSyntheticMessage(&dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(busPath),
			dbus.FieldInterface:   dbus.MakeVariant(busName),
			dbus.FieldMember:      dbus.MakeVariant(member),
			dbus.FieldSender:      dbus.MakeVariant(busName),
			dbus.FieldDestination: dbus.MakeVariant(destination),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(name)),
		},
		Body: []any{name},
	})
}

func singleString(body []any) (string, bool) {
	if len(body) != 1 {
		return "", false
	}
	value, ok := body[0].(string)
	return value, ok
}

func (b *portalBus) portalMethod(ctx context.Context, client *serializedConn, connection *portalConnection, call *dbus.Message, path dbus.ObjectPath, interfaceName, member string) {
	options := b.proxy.options
	if interfaceName == requestInterface && member == "Close" {
		b.proxy.cancelRequest(path)
		b.reply(client, connection, call, portalUniqueName)
		return
	}
	if path != portalObjectPath {
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "no such object")
		return
	}
	switch interfaceName {
	case propertiesInterface:
		b.portalProperties(client, connection, call, member)
	case introspectInterface:
		if member != "Introspect" {
			b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
			return
		}
		b.reply(client, connection, call, portalUniqueName, portalIntrospection())
	case peerInterface:
		b.reply(client, connection, call, portalUniqueName)
	case fileChooserInterface:
		if !options.FilePicker || member != "OpenFile" && member != "SaveFile" {
			b.fail(client, connection, call, portalUniqueName, errorNotAllowed, "this file chooser call is not provided by cpak")
			return
		}
		request, err := decodeFileChooserCall(member, call.Body)
		if err != nil {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, err.Error())
			return
		}
		handle := portalRequestPath(connection.name(), call)
		requestCtx, cancel := context.WithCancel(ctx)
		b.proxy.storeRequest(handle, cancel)
		b.reply(client, connection, call, portalUniqueName, handle)
		go b.proxy.pick(requestCtx, client, handle, connection.name(), request)
	case openURIInterface:
		b.openURI(ctx, client, connection, call, member)
	case settingsInterface:
		b.settings(client, connection, call, member)
	case networkInterface:
		b.networkMonitor(client, connection, call, member)
	case notificationPortal:
		b.portalNotification(ctx, client, connection, call, member)
	case proxyInterface:
		// GLib resolves every proxy through this portal inside a Flatpak
		// sandbox. The sandbox reaches the network through its own stack,
		// which no host proxy setting applies to, so the answer is a direct
		// connection.
		if member != "Lookup" || len(call.Body) != 1 {
			b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
			return
		}
		b.reply(client, connection, call, portalUniqueName, []string{"direct://"})
	default:
		b.fail(client, connection, call, portalUniqueName, errorNotAllowed, "this portal is not provided by cpak")
	}
}

func (b *portalBus) portalProperties(client *serializedConn, connection *portalConnection, call *dbus.Message, member string) {
	switch member {
	case "Get":
		if len(call.Body) != 2 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected an interface and a property")
			return
		}
		interfaceName, _ := call.Body[0].(string)
		property, _ := call.Body[1].(string)
		version, known := portalVersions[interfaceName]
		if !known || property != "version" {
			b.fail(client, connection, call, portalUniqueName, "org.freedesktop.DBus.Error.UnknownProperty", "unknown property")
			return
		}
		b.reply(client, connection, call, portalUniqueName, dbus.MakeVariant(version))
	case "GetAll":
		interfaceName, ok := singleString(call.Body)
		if !ok {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected an interface")
			return
		}
		values := map[string]dbus.Variant{}
		if version, known := portalVersions[interfaceName]; known {
			values["version"] = dbus.MakeVariant(version)
		}
		b.reply(client, connection, call, portalUniqueName, values)
	default:
		b.fail(client, connection, call, portalUniqueName, errorAccessDenied, "portal properties are read-only")
	}
}

// portalRequestPath names the request object the way the portal
// specification does, so a toolkit that subscribed to the Response signal on
// the path it predicted from its handle_token receives it.
func portalRequestPath(unique string, call *dbus.Message) dbus.ObjectPath {
	state := &connectionState{uniqueName: unique}
	return state.requestPath(call)
}

func (b *portalBus) openURI(ctx context.Context, client *serializedConn, connection *portalConnection, call *dbus.Message, member string) {
	if member != "OpenURI" && member != "OpenFile" && member != "OpenDirectory" {
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
		return
	}
	if len(call.Body) != 3 {
		b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected a parent window, a target and options")
		return
	}
	handle := portalRequestPath(connection.name(), call)
	b.reply(client, connection, call, portalUniqueName, handle)
	destination := connection.name()
	uri, _ := call.Body[1].(string)
	if member != "OpenURI" || !b.proxy.options.OpenURI || uri == "" {
		// A file passed by descriptor names a file inside the sandbox, which
		// the host has no path for; cpak opens links, and files the user
		// chose through the file chooser are already the application's.
		_ = client.writeSyntheticMessage(responseSignal(handle, portalUniqueName, destination, 2, map[string]dbus.Variant{}))
		return
	}
	request := systembroker.OpenURIRequest{URI: uri}
	if options, ok := call.Body[2].(map[string]dbus.Variant); ok {
		request.ActivationToken, _ = variantValue[string](options, "activation_token")
	}
	go func() {
		response := uint32(0)
		if err := b.proxy.openURI(ctx, request); err != nil {
			logger.Printf("Opening a link failed: %v", err)
			response = 2
		}
		_ = client.writeSyntheticMessage(responseSignal(handle, portalUniqueName, destination, response, map[string]dbus.Variant{}))
	}()
}

// settingsNamespaces are what the settings portal answers: the appearance
// every toolkit reads, and the interface settings GTK and libadwaita take the
// icon theme from. Nothing else of the host's configuration is visible.
func (b *portalBus) settingsNamespaces() map[string]map[string]dbus.Variant {
	interfaceSettings := map[string]dbus.Variant{}
	if theme := b.proxy.options.IconTheme; theme != "" {
		interfaceSettings["icon-theme"] = dbus.MakeVariant(theme)
	}
	scheme := "default"
	switch b.proxy.options.ColorScheme {
	case 1:
		scheme = "prefer-dark"
	case 2:
		scheme = "prefer-light"
	}
	interfaceSettings["color-scheme"] = dbus.MakeVariant(scheme)
	return map[string]map[string]dbus.Variant{
		appearanceNamespace: {
			"color-scheme": dbus.MakeVariant(b.proxy.options.ColorScheme),
			"contrast":     dbus.MakeVariant(uint32(0)),
		},
		interfaceNamespace: interfaceSettings,
	}
}

func namespaceMatches(pattern, namespace string) bool {
	if pattern == "" || pattern == namespace {
		return true
	}
	prefix, wildcard := strings.CutSuffix(pattern, "*")
	return wildcard && strings.HasPrefix(namespace, prefix)
}

func (b *portalBus) settings(client *serializedConn, connection *portalConnection, call *dbus.Message, member string) {
	namespaces := b.settingsNamespaces()
	switch member {
	case "Read", "ReadOne":
		if len(call.Body) != 2 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected a namespace and a key")
			return
		}
		namespace, _ := call.Body[0].(string)
		key, _ := call.Body[1].(string)
		value, found := namespaces[namespace][key]
		if !found {
			b.fail(client, connection, call, portalUniqueName, errorPortalNotFound, "requested setting not found")
			return
		}
		if member == "Read" {
			// Read predates ReadOne and wraps its value in a second variant,
			// which is what the toolkits that still call it unwrap.
			value = dbus.MakeVariant(value)
		}
		b.reply(client, connection, call, portalUniqueName, value)
	case "ReadAll":
		if len(call.Body) != 1 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected namespaces")
			return
		}
		patterns, _ := call.Body[0].([]string)
		if len(patterns) == 0 {
			patterns = []string{""}
		}
		result := map[string]map[string]dbus.Variant{}
		for name, values := range namespaces {
			for _, pattern := range patterns {
				if namespaceMatches(pattern, name) {
					result[name] = values
				}
			}
		}
		b.reply(client, connection, call, portalUniqueName, result)
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
	}
}

// networkMonitor answers from the application's policy rather than from the
// host network: with network access the sandbox reaches the internet through
// its own stack, without it nothing beyond itself.
func (b *portalBus) networkMonitor(client *serializedConn, connection *portalConnection, call *dbus.Message, member string) {
	available := b.proxy.options.NetworkMonitor
	connectivity := uint32(1)
	if available {
		connectivity = 4
	}
	switch member {
	case "GetAvailable":
		b.reply(client, connection, call, portalUniqueName, available)
	case "GetMetered":
		b.reply(client, connection, call, portalUniqueName, false)
	case "GetConnectivity":
		b.reply(client, connection, call, portalUniqueName, connectivity)
	case "GetStatus":
		b.reply(client, connection, call, portalUniqueName, map[string]dbus.Variant{
			"available":    dbus.MakeVariant(available),
			"metered":      dbus.MakeVariant(false),
			"connectivity": dbus.MakeVariant(connectivity),
		})
	case "CanReach":
		b.reply(client, connection, call, portalUniqueName, available)
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
	}
}

func (b *portalBus) portalNotification(ctx context.Context, client *serializedConn, connection *portalConnection, call *dbus.Message, member string) {
	switch member {
	case "AddNotification":
		if !b.proxy.options.Notifications {
			b.fail(client, connection, call, portalUniqueName, errorNotAllowed, "notifications are not permitted")
			return
		}
		if len(call.Body) != 2 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "expected an id and a notification")
			return
		}
		values, ok := call.Body[1].(map[string]dbus.Variant)
		if !ok {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "invalid notification")
			return
		}
		request := systembroker.NotificationRequest{AppName: b.proxy.options.AppName, ExpireTimeout: -1}
		request.Summary, _ = variantValue[string](values, "title")
		request.Body, _ = variantValue[string](values, "body")
		if priority, _ := variantValue[string](values, "priority"); priority == "urgent" {
			request.Urgency = "critical"
		} else if priority == "low" {
			request.Urgency = "low"
		}
		if request.Summary == "" {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "a notification needs a title")
			return
		}
		b.reply(client, connection, call, portalUniqueName)
		go b.deliver(ctx, request)
	case "RemoveNotification":
		b.reply(client, connection, call, portalUniqueName)
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
	}
}

func (b *portalBus) notificationsMethod(ctx context.Context, client *serializedConn, connection *portalConnection, call *dbus.Message, path dbus.ObjectPath, interfaceName, member string) {
	if path != notificationsPath || interfaceName != notificationsName && interfaceName != "" {
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "no such object")
		return
	}
	switch member {
	case "GetCapabilities":
		b.reply(client, connection, call, portalUniqueName, []string{"body"})
	case "GetServerInformation":
		b.reply(client, connection, call, portalUniqueName, "cpak", "Fabricators", "1", "1.2")
	case "CloseNotification":
		b.reply(client, connection, call, portalUniqueName)
	case "Notify":
		if len(call.Body) != 8 {
			b.fail(client, connection, call, portalUniqueName, errorInvalidArgs, "unexpected notification arguments")
			return
		}
		request := systembroker.NotificationRequest{AppName: b.proxy.options.AppName}
		appName, _ := call.Body[0].(string)
		if request.AppName == "" {
			request.AppName = appName
		}
		request.ReplaceID, _ = call.Body[1].(uint32)
		request.Summary, _ = call.Body[3].(string)
		request.Body, _ = call.Body[4].(string)
		request.ExpireTimeout, _ = call.Body[7].(int32)
		if hints, ok := call.Body[6].(map[string]dbus.Variant); ok {
			if urgency, ok := variantValue[byte](hints, "urgency"); ok {
				request.Urgency = [...]string{"low", "normal", "critical"}[min(int(urgency), 2)]
			}
			request.Category, _ = variantValue[string](hints, "category")
			request.Transient, _ = variantValue[bool](hints, "transient")
		}
		id := request.ReplaceID
		if id == 0 {
			id = b.notifyID.Add(1)
		}
		b.reply(client, connection, call, portalUniqueName, id)
		go b.deliver(ctx, request)
	default:
		b.fail(client, connection, call, portalUniqueName, errorUnknownMethod, "unknown method")
	}
}

func (b *portalBus) deliver(ctx context.Context, request systembroker.NotificationRequest) {
	if err := b.proxy.notify(ctx, request); err != nil {
		logger.Printf("Notification failed: %v", err)
	}
}

func (p *Proxy) broker() systembroker.Client {
	return systembroker.Client{SocketPath: p.options.BrokerSocketPath, Token: p.options.BrokerToken}
}

func (p *Proxy) notify(ctx context.Context, request systembroker.NotificationRequest) error {
	if p.options.Notify != nil {
		return p.options.Notify(ctx, request)
	}
	return p.broker().Notify(ctx, request)
}

func (p *Proxy) openURI(ctx context.Context, request systembroker.OpenURIRequest) error {
	if p.options.OpenURIHandler != nil {
		return p.options.OpenURIHandler(ctx, request)
	}
	return p.broker().OpenURI(ctx, request)
}

func portalIntrospection() string {
	var out strings.Builder
	out.WriteString(`<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd">` + "\n<node>\n")
	for _, name := range []string{fileChooserInterface, openURIInterface, settingsInterface, networkInterface, notificationPortal, proxyInterface} {
		fmt.Fprintf(&out, "  <interface name=%q><property name=\"version\" type=\"u\" access=\"read\"/></interface>\n", name)
	}
	out.WriteString("</node>\n")
	return out.String()
}
