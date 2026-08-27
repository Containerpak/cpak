/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/desktopui"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	portalDestination    = "org.freedesktop.portal.Desktop"
	fileChooserInterface = "org.freedesktop.portal.FileChooser"
	requestInterface     = "org.freedesktop.portal.Request"
	portalObjectPath     = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	requestPathPrefix    = "/org/freedesktop/portal/desktop/request/cpak/"
	maxPortalSelections  = 128
)

type Options struct {
	SocketPath       string
	UpstreamAddress  string
	BrokerSocketPath string
	BrokerToken      string
	FilePicker       bool
	Policy           types.DBusPolicy
	PickFile         func(context.Context, systembroker.FilePickerRequest) (systembroker.FilePickerResult, error)
}

type Proxy struct {
	options      Options
	portalSender string
	mu           sync.Mutex
	requests     map[dbus.ObjectPath]context.CancelFunc
}

type connectionState struct {
	mu          sync.RWMutex
	uniqueName  string
	helloSerial uint32
}

func Serve(ctx context.Context, options Options) error {
	if err := validateOptions(options); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.SocketPath), 0700); err != nil {
		return fmt.Errorf("create desktop bus directory: %w", err)
	}
	if err := os.Remove(options.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale desktop bus socket: %w", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: options.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen for desktop bus: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(options.SocketPath)
	}()
	if err = os.Chmod(options.SocketPath, 0600); err != nil {
		return fmt.Errorf("restrict desktop bus socket: %w", err)
	}
	portalSender := portalDestination
	if owner, ownerErr := resolveBusNameOwner(options.UpstreamAddress, portalDestination); ownerErr == nil {
		portalSender = owner
	}
	proxy := &Proxy{options: options, portalSender: portalSender, requests: map[dbus.ObjectPath]context.CancelFunc{}}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	defer close(done)
	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept desktop bus connection: %w", acceptErr)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			proxy.serveConnection(ctx, connection)
		}()
	}
}

func validateOptions(options Options) error {
	if !filepath.IsAbs(options.SocketPath) || options.UpstreamAddress == "" {
		return errors.New("desktop bus socket and upstream address are required")
	}
	if err := types.ValidateDBusPolicy(options.Policy); err != nil {
		return err
	}
	if options.FilePicker && options.PickFile == nil && (!filepath.IsAbs(options.BrokerSocketPath) || len(options.BrokerToken) < 32) {
		return errors.New("desktop bus broker credentials are invalid")
	}
	return nil
}

func (p *Proxy) serveConnection(parent context.Context, raw *net.UnixConn) {
	defer raw.Close()
	if err := authenticateClient(raw); err != nil {
		return
	}
	upstream, err := dialUpstream(p.options.UpstreamAddress)
	if err != nil {
		return
	}
	defer upstream.Close()
	if err = authenticateUpstream(upstream); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	client := &serializedConn{UnixConn: raw}
	client.serial.Store(1 << 30)
	state := &connectionState{}
	errorsChannel := make(chan error, 2)
	go func() {
		for {
			packet, readErr := readMessage(upstream)
			if readErr != nil {
				errorsChannel <- fmt.Errorf("read upstream bus: %w", readErr)
				return
			}
			message := packet.message
			state.observeUpstream(message)
			if !restrictedUpstreamMessage(message, p.options.Policy) {
				packet.closeFDs()
				continue
			}
			if writeErr := client.writePacket(packet); writeErr != nil {
				packet.closeFDs()
				errorsChannel <- fmt.Errorf("write client bus: %w", writeErr)
				return
			}
			packet.closeFDs()
		}
	}()
	go func() {
		for {
			packet, readErr := readMessage(raw)
			if readErr != nil {
				errorsChannel <- fmt.Errorf("read client bus: %w", readErr)
				return
			}
			state.observeClient(packet.message)
			if p.intercept(ctx, client, state, packet.message) {
				packet.closeFDs()
				continue
			}
			// The policy decision is made from parsed header fields. Rebuild that
			// header before forwarding it, while preserving the original body
			// because godbus cannot re-encode every valid typed empty array.
			writeErr := writeCanonicalPacket(upstream, packet)
			packet.closeFDs()
			if writeErr != nil {
				errorsChannel <- fmt.Errorf("write upstream bus: %w", writeErr)
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
	case connectionErr := <-errorsChannel:
		if !errors.Is(connectionErr, io.EOF) && !errors.Is(connectionErr, net.ErrClosed) {
			logger.Printf("Desktop bus connection closed: %v", connectionErr)
		}
	}
}

func dialUpstream(address string) (*net.UnixConn, error) {
	var lastErr error
	for _, candidate := range strings.Split(address, ";") {
		if !strings.HasPrefix(candidate, "unix:") {
			continue
		}
		parameters := strings.Split(strings.TrimPrefix(candidate, "unix:"), ",")
		for _, parameter := range parameters {
			pair := strings.SplitN(parameter, "=", 2)
			if len(pair) != 2 || pair[0] != "path" && pair[0] != "abstract" {
				continue
			}
			name, err := url.PathUnescape(pair[1])
			if err != nil {
				return nil, fmt.Errorf("decode desktop bus address: %w", err)
			}
			if pair[0] == "abstract" {
				name = "@" + name
			}
			connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: name, Net: "unix"})
			if err != nil {
				lastErr = err
				continue
			}
			return connection, nil
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("connect to desktop bus: %w", lastErr)
	}
	return nil, errors.New("desktop bus address is not a supported Unix socket")
}

func resolveBusNameOwner(address, name string) (string, error) {
	connection, err := dbus.Connect(address)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	var owner string
	if err = connection.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
		return "", err
	}
	if !strings.HasPrefix(owner, ":") {
		return "", errors.New("desktop portal owner is invalid")
	}
	return owner, nil
}

func (p *Proxy) intercept(ctx context.Context, client *serializedConn, state *connectionState, message *dbus.Message) bool {
	if message.Type != dbus.TypeMethodCall {
		// Default-deny used to cover method calls and nothing else, so a
		// confined client's signals, replies and errors went to the real
		// session bus carrying its unique name as sender. The policy is the
		// same for every type it can emit. There is nothing to answer a signal
		// with, so it is dropped rather than refused.
		return true
	}
	path, _ := headerValue[dbus.ObjectPath](message, dbus.FieldPath)
	interfaceName, _ := headerValue[string](message, dbus.FieldInterface)
	member, _ := headerValue[string](message, dbus.FieldMember)
	destination, _ := headerValue[string](message, dbus.FieldDestination)
	if interfaceName == requestInterface && member == "Close" && p.hasRequest(path) {
		p.cancelRequest(path)
		_ = client.writeSyntheticMessage(methodReply(message, nil))
		return true
	}
	if p.options.FilePicker && p.portalDestination(destination) && path == portalObjectPath && interfaceName == fileChooserInterface && (member == "OpenFile" || member == "SaveFile") {
		request, err := decodeFileChooserCall(member, message.Body)
		if err != nil {
			_ = client.writeSyntheticMessage(methodError(message, "org.freedesktop.DBus.Error.InvalidArgs", err.Error()))
			return true
		}
		handle := state.requestPath(message)
		requestCtx, cancel := context.WithCancel(ctx)
		p.storeRequest(handle, cancel)
		if err = client.writeSyntheticMessage(methodReply(message, []any{handle})); err != nil {
			p.cancelRequest(handle)
			return true
		}
		go p.pick(requestCtx, client, handle, state.clientName(), request)
		return true
	}
	if restrictedBusMatchCall(destination, path, interfaceName, member) {
		_ = client.writeSyntheticMessage(methodReply(message, nil))
		return true
	}
	if restrictedBusCallAllowed(destination, p.portalSender, path, interfaceName, member, message.Body) {
		return false
	}
	if policyBusCallAllowed(p.options.Policy, destination, path, interfaceName, member, message.Body) {
		return false
	}
	_ = client.writeSyntheticMessage(methodError(message, "org.freedesktop.DBus.Error.AccessDenied", "session bus access is not permitted"))
	return true
}

func restrictedBusMatchCall(destination string, path dbus.ObjectPath, interfaceName, member string) bool {
	return destination == "org.freedesktop.DBus" && path == dbus.ObjectPath("/org/freedesktop/DBus") && interfaceName == "org.freedesktop.DBus" && (member == "AddMatch" || member == "RemoveMatch")
}

func restrictedBusCallAllowed(destination, portalSender string, path dbus.ObjectPath, interfaceName, member string, body []any) bool {
	if destination == "org.freedesktop.DBus" && path == dbus.ObjectPath("/org/freedesktop/DBus") && interfaceName == "org.freedesktop.DBus" {
		switch member {
		case "Hello", "GetId":
			return true
		case "GetNameOwner", "NameHasOwner":
			return len(body) == 1 && body[0] == portalDestination
		}
	}
	if destination != portalDestination && destination != portalSender || path != portalObjectPath {
		return false
	}
	switch interfaceName {
	case "org.freedesktop.DBus.Properties":
		return member == "Get" || member == "GetAll"
	case "org.freedesktop.DBus.Introspectable":
		return member == "Introspect"
	case "org.freedesktop.DBus.Peer":
		return member == "Ping" || member == "GetMachineId"
	}
	return false
}

func (p *Proxy) portalDestination(destination string) bool {
	return destination == portalDestination || destination == p.portalSender
}

func policyBusCallAllowed(policy types.DBusPolicy, destination string, path dbus.ObjectPath, interfaceName, member string, body []any) bool {
	if destination == "org.freedesktop.DBus" && path == dbus.ObjectPath("/org/freedesktop/DBus") && interfaceName == "org.freedesktop.DBus" {
		if member == "RequestName" && len(body) == 2 {
			name, ok := body[0].(string)
			return ok && policy.AllowsOwn(name)
		}
		if (member == "ReleaseName" || member == "GetNameOwner" || member == "NameHasOwner") && len(body) == 1 {
			name, ok := body[0].(string)
			return ok && policy.AllowsOwn(name)
		}
	}
	return policy.AllowsCall(destination, string(path), interfaceName, member)
}

func restrictedUpstreamMessage(message *dbus.Message, policy types.DBusPolicy) bool {
	if message.Type == dbus.TypeMethodReply || message.Type == dbus.TypeError {
		return true
	}
	if message.Type != dbus.TypeSignal {
		return false
	}
	interfaceName, _ := headerValue[string](message, dbus.FieldInterface)
	member, _ := headerValue[string](message, dbus.FieldMember)
	if interfaceName != "org.freedesktop.DBus" || member != "NameAcquired" && member != "NameLost" || len(message.Body) != 1 {
		return false
	}
	name, ok := message.Body[0].(string)
	return ok && (strings.HasPrefix(name, ":") || policy.AllowsOwn(name))
}

func (s *connectionState) observeClient(message *dbus.Message) {
	if message.Type != dbus.TypeMethodCall {
		return
	}
	destination, _ := headerValue[string](message, dbus.FieldDestination)
	interfaceName, _ := headerValue[string](message, dbus.FieldInterface)
	member, _ := headerValue[string](message, dbus.FieldMember)
	if destination == "org.freedesktop.DBus" && interfaceName == "org.freedesktop.DBus" && member == "Hello" {
		s.mu.Lock()
		s.helloSerial = message.Serial()
		s.mu.Unlock()
	}
}

func (s *connectionState) observeUpstream(message *dbus.Message) {
	if message.Type != dbus.TypeMethodReply || len(message.Body) != 1 {
		return
	}
	replySerial, _ := headerValue[uint32](message, dbus.FieldReplySerial)
	name, ok := message.Body[0].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ok && replySerial == s.helloSerial && strings.HasPrefix(name, ":") {
		s.uniqueName = name
	}
}

func (s *connectionState) requestPath(message *dbus.Message) dbus.ObjectPath {
	token := ""
	if len(message.Body) == 3 {
		if options, ok := message.Body[2].(map[string]dbus.Variant); ok {
			token, _ = variantValue[string](options, "handle_token")
		}
	}
	s.mu.RLock()
	name := s.uniqueName
	s.mu.RUnlock()
	if validHandlePart(token) && strings.HasPrefix(name, ":") {
		sender := strings.NewReplacer(":", "", ".", "_").Replace(name)
		if validHandlePart(sender) {
			return dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
		}
	}
	return dbus.ObjectPath(fmt.Sprintf("%s%x", requestPathPrefix, message.Serial()))
}

func (s *connectionState) clientName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uniqueName
}

func validHandlePart(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char != '_' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func (p *Proxy) pick(ctx context.Context, client *serializedConn, handle dbus.ObjectPath, destination string, request systembroker.FilePickerRequest) {
	defer p.finishRequest(handle)
	pickFile := p.options.PickFile
	if pickFile == nil {
		broker := systembroker.Client{SocketPath: p.options.BrokerSocketPath, Token: p.options.BrokerToken}
		pickFile = broker.FilePicker
	}
	result, err := pickFile(ctx, request)
	response := uint32(0)
	values := map[string]dbus.Variant{}
	if err != nil {
		logger.Printf("File picker failed: %v", err)
		response = 2
		if errors.Is(ctx.Err(), context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "cancel") {
			response = 1
		}
	} else {
		if len(result.Paths) == 0 && result.Path != "" {
			result.Paths = []string{result.Path}
		}
		if len(result.Paths) == 0 || len(result.Paths) > maxPortalSelections {
			response = 2
			result.Paths = nil
		}
		uris := make([]string, 0, len(result.Paths))
		for _, path := range result.Paths {
			uris = append(uris, (&url.URL{Scheme: "file", Path: path}).String())
		}
		values["uris"] = dbus.MakeVariant(uris)
		values["writable"] = dbus.MakeVariant(result.Access == "read-write")
	}
	_ = client.writeSyntheticMessage(responseSignal(handle, p.portalSender, destination, response, values))
}

func decodeFileChooserCall(member string, body []any) (systembroker.FilePickerRequest, error) {
	if len(body) != 3 {
		return systembroker.FilePickerRequest{}, errors.New("file chooser expects parent, title and options")
	}
	parent, ok := body[0].(string)
	if !ok {
		return systembroker.FilePickerRequest{}, errors.New("file chooser parent is invalid")
	}
	title, ok := body[1].(string)
	if !ok {
		return systembroker.FilePickerRequest{}, errors.New("file chooser title is invalid")
	}
	options, ok := body[2].(map[string]dbus.Variant)
	if !ok {
		return systembroker.FilePickerRequest{}, errors.New("file chooser options are invalid")
	}
	request := systembroker.FilePickerRequest{Mode: desktopui.PickerOpenFile, ParentWindow: parent, Title: title}
	if member == "SaveFile" {
		request.Mode = desktopui.PickerSaveFile
	}
	request.AcceptLabel, _ = variantValue[string](options, "accept_label")
	request.SuggestedName, _ = variantValue[string](options, "current_name")
	if currentFolder, found := options["current_folder"]; found {
		request.CurrentFolder = decodePortalPath(currentFolder.Value())
	}
	request.Multiple, _ = variantValue[bool](options, "multiple")
	directory, _ := variantValue[bool](options, "directory")
	if directory {
		request.Mode = desktopui.PickerOpenFolder
		request.Multiple = false
	}
	if filters, found := options["filters"]; found {
		request.Filters = decodeFilters(filters.Value())
	}
	if len(request.Filters) > 32 {
		return systembroker.FilePickerRequest{}, errors.New("file chooser has too many filters")
	}
	return request, nil
}

func decodePortalPath(value any) string {
	encoded, ok := value.([]byte)
	if !ok || len(encoded) == 0 || encoded[len(encoded)-1] != 0 {
		return ""
	}
	path := string(encoded[:len(encoded)-1])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00') {
		return ""
	}
	return path
}

type decodedFilter struct {
	Name  string
	Rules []decodedFilterRule
}

type decodedFilterRule struct {
	Kind    uint32
	Pattern string
}

func decodeFilters(value any) []systembroker.FilePickerFilter {
	decoded := []decodedFilter{}
	if err := dbus.Store([]any{value}, &decoded); err != nil {
		return nil
	}
	filters := make([]systembroker.FilePickerFilter, 0, len(decoded))
	for _, filter := range decoded {
		current := systembroker.FilePickerFilter{Name: filter.Name}
		for _, rule := range filter.Rules {
			switch rule.Kind {
			case 0:
				current.Patterns = append(current.Patterns, rule.Pattern)
			case 1:
				current.MIMETypes = append(current.MIMETypes, rule.Pattern)
			}
		}
		if len(current.Patterns)+len(current.MIMETypes) > 0 {
			filters = append(filters, current)
		}
	}
	return filters
}

func methodReply(call *dbus.Message, body []any) *dbus.Message {
	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldReplySerial: dbus.MakeVariant(call.Serial()),
	}
	if len(body) > 0 {
		headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	return &dbus.Message{Type: dbus.TypeMethodReply, Headers: headers, Body: body}
}

func methodError(call *dbus.Message, name, detail string) *dbus.Message {
	return &dbus.Message{
		Type: dbus.TypeError,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldErrorName:   dbus.MakeVariant(name),
			dbus.FieldReplySerial: dbus.MakeVariant(call.Serial()),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(detail)),
		},
		Body: []any{detail},
	}
}

func responseSignal(path dbus.ObjectPath, sender, destination string, response uint32, values map[string]dbus.Variant) *dbus.Message {
	return &dbus.Message{
		Type: dbus.TypeSignal,
		Headers: map[dbus.HeaderField]dbus.Variant{
			dbus.FieldPath:        dbus.MakeVariant(path),
			dbus.FieldInterface:   dbus.MakeVariant(requestInterface),
			dbus.FieldMember:      dbus.MakeVariant("Response"),
			dbus.FieldSender:      dbus.MakeVariant(sender),
			dbus.FieldDestination: dbus.MakeVariant(destination),
			dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(response, values)),
		},
		Body: []any{response, values},
	}
}

func headerValue[T any](message *dbus.Message, field dbus.HeaderField) (T, bool) {
	var zero T
	variant, found := message.Headers[field]
	if !found {
		return zero, false
	}
	value, ok := variant.Value().(T)
	return value, ok
}

func variantValue[T any](values map[string]dbus.Variant, key string) (T, bool) {
	var zero T
	variant, found := values[key]
	if !found {
		return zero, false
	}
	value, ok := variant.Value().(T)
	return value, ok
}

func (p *Proxy) storeRequest(path dbus.ObjectPath, cancel context.CancelFunc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests[path] = cancel
}

func (p *Proxy) hasRequest(path dbus.ObjectPath) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, found := p.requests[path]
	return found
}

func (p *Proxy) cancelRequest(path dbus.ObjectPath) {
	p.mu.Lock()
	cancel := p.requests[path]
	delete(p.requests, path)
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *Proxy) finishRequest(path dbus.ObjectPath) {
	p.mu.Lock()
	delete(p.requests, path)
	p.mu.Unlock()
}
