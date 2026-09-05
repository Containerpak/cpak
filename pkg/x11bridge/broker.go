/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package x11bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const (
	windowPollInterval = 100 * time.Millisecond
	windowCloseGrace   = 2 * time.Second
)

type Options struct {
	Nested        *xgb.Conn
	Host          *xgb.Conn
	HostWindow    string
	HostToApp     bool
	AppToHost     bool
	ServerAlive   func() bool
	StopContainer func()
	Ready         func() error
}

type endpoint struct {
	connection *xgb.Conn
	root       xproto.Window
	window     xproto.Window
	atoms      endpointAtoms
	selection  sync.Mutex
}

type endpointAtoms struct {
	clipboard            xproto.Atom
	primary              xproto.Atom
	targets              xproto.Atom
	incr                 xproto.Atom
	utf8                 xproto.Atom
	netWMName            xproto.Atom
	netWMIcon            xproto.Atom
	netWMState           xproto.Atom
	netWMStateFullscreen xproto.Atom
	netWMStateMaxHorz    xproto.Atom
	netWMStateMaxVert    xproto.Atom
	netWMWindowType      xproto.Atom
	netWMWindowNormal    xproto.Atom
	netWMWindowDialog    xproto.Atom
	netWMSupported       xproto.Atom
	netWMSupportingWM    xproto.Atom
	netWMActiveWindow    xproto.Atom
	wmSelection          xproto.Atom
}

type broker struct {
	options    Options
	nested     *endpoint
	host       *endpoint
	hostWindow xproto.Window
	primary    xproto.Window
	seenWindow bool
	closeAt    time.Time
	lastTitle  string
	lastIcon   []byte
	fullscreen bool
	maximized  bool
	clipboard  *clipboardBridge
	events     chan endpointEvent
}

type endpointEvent struct {
	endpoint *endpoint
	event    xgb.Event
	err      error
}

func Run(ctx context.Context, options Options) error {
	if options.Nested == nil || options.ServerAlive == nil || options.StopContainer == nil {
		return errors.New("invalid X11 broker configuration")
	}
	nested, err := newEndpoint(options.Nested, true)
	if err != nil {
		return fmt.Errorf("prepare isolated X11 display: %w", err)
	}
	var host *endpoint
	if options.Host != nil {
		host, err = newEndpoint(options.Host, false)
		if err != nil {
			return fmt.Errorf("prepare host X11 display: %w", err)
		}
	}
	b := &broker{
		options: options,
		nested:  nested,
		host:    host,
		events:  make(chan endpointEvent, 128),
	}
	b.clipboard = newClipboardBridge(nested, host, options.HostToApp, options.AppToHost)
	if options.Ready != nil {
		if err = options.Ready(); err != nil {
			return fmt.Errorf("report X11 broker readiness: %w", err)
		}
	}
	b.watch(nested)
	if host != nil {
		b.watch(host)
	}
	ticker := time.NewTicker(windowPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case received := <-b.events:
			if received.err != nil {
				if received.endpoint == nested {
					b.options.StopContainer()
					return nil
				}
				b.host = nil
				b.clipboard.host = nil
				b.clipboard.hostToApp = false
				b.clipboard.appToHost = false
				continue
			}
			b.handleEvent(received)
		case <-ticker.C:
			if !b.options.ServerAlive() {
				b.options.StopContainer()
				return nil
			}
			if b.tick() {
				b.options.StopContainer()
				return nil
			}
		}
	}
}

func newEndpoint(connection *xgb.Conn, windowManager bool) (*endpoint, error) {
	setup := xproto.Setup(connection)
	if setup == nil || len(setup.Roots) == 0 {
		return nil, errors.New("X11 display has no screen")
	}
	root := setup.DefaultScreen(connection).Root
	window, err := xproto.NewWindowId(connection)
	if err != nil {
		return nil, err
	}
	if err = xproto.CreateWindowChecked(
		connection,
		setup.DefaultScreen(connection).RootDepth,
		window,
		root,
		-1,
		-1,
		1,
		1,
		0,
		xproto.WindowClassInputOutput,
		setup.DefaultScreen(connection).RootVisual,
		xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange},
	).Check(); err != nil {
		return nil, err
	}
	atoms, err := loadAtoms(connection)
	if err != nil {
		return nil, err
	}
	mask := uint32(xproto.EventMaskSubstructureNotify | xproto.EventMaskStructureNotify | xproto.EventMaskPropertyChange)
	if windowManager {
		mask |= xproto.EventMaskSubstructureRedirect
	}
	if err = xproto.ChangeWindowAttributesChecked(connection, root, xproto.CwEventMask, []uint32{mask}).Check(); err != nil {
		return nil, err
	}
	result := &endpoint{connection: connection, root: root, window: window, atoms: atoms}
	if windowManager {
		if err = result.publishWindowManager(); err != nil {
			return nil, err
		}
	}
	connection.Sync()
	return result, nil
}

func loadAtoms(connection *xgb.Conn) (endpointAtoms, error) {
	names := []string{
		"CLIPBOARD", "PRIMARY", "TARGETS", "INCR", "UTF8_STRING",
		"_NET_WM_NAME", "_NET_WM_ICON", "_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN",
		"_NET_WM_STATE_MAXIMIZED_HORZ", "_NET_WM_STATE_MAXIMIZED_VERT",
		"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL", "_NET_WM_WINDOW_TYPE_DIALOG",
		"_NET_SUPPORTED", "_NET_SUPPORTING_WM_CHECK", "_NET_ACTIVE_WINDOW", "WM_S0",
	}
	values := make([]xproto.Atom, len(names))
	for index, name := range names {
		reply, err := xproto.InternAtom(connection, false, uint16(len(name)), name).Reply()
		if err != nil {
			return endpointAtoms{}, err
		}
		values[index] = reply.Atom
	}
	return endpointAtoms{
		clipboard: values[0], primary: values[1], targets: values[2], incr: values[3], utf8: values[4],
		netWMName: values[5], netWMIcon: values[6], netWMState: values[7], netWMStateFullscreen: values[8],
		netWMStateMaxHorz: values[9], netWMStateMaxVert: values[10], netWMWindowType: values[11],
		netWMWindowNormal: values[12], netWMWindowDialog: values[13], netWMSupported: values[14], netWMSupportingWM: values[15],
		netWMActiveWindow: values[16], wmSelection: values[17],
	}, nil
}

func (e *endpoint) publishWindowManager() error {
	setProperty32(e.connection, e.root, e.atoms.netWMSupportingWM, xproto.AtomWindow, uint32(e.window))
	setProperty32(e.connection, e.window, e.atoms.netWMSupportingWM, xproto.AtomWindow, uint32(e.window))
	supported := []uint32{
		uint32(e.atoms.netWMState),
		uint32(e.atoms.netWMStateFullscreen),
		uint32(e.atoms.netWMStateMaxHorz),
		uint32(e.atoms.netWMStateMaxVert),
		uint32(e.atoms.netWMActiveWindow),
	}
	setProperty32(e.connection, e.root, e.atoms.netWMSupported, xproto.AtomAtom, supported...)
	name := []byte("cpak")
	xproto.ChangeProperty(e.connection, xproto.PropModeReplace, e.window, e.atoms.netWMName, e.atoms.utf8, 8, uint32(len(name)), name)
	if err := xproto.SetSelectionOwnerChecked(e.connection, e.window, e.atoms.wmSelection, xproto.TimeCurrentTime).Check(); err != nil {
		return fmt.Errorf("claim X11 window manager selection: %w", err)
	}
	owner, err := xproto.GetSelectionOwner(e.connection, e.atoms.wmSelection).Reply()
	if err != nil {
		return fmt.Errorf("read X11 window manager selection: %w", err)
	}
	if owner.Owner != e.window {
		return errors.New("claim X11 window manager selection: another window manager is active")
	}
	return nil
}

func (b *broker) watch(display *endpoint) {
	go func() {
		for {
			event, err := display.connection.WaitForEvent()
			if event == nil {
				connectionErr := error(err)
				if connectionErr == nil {
					connectionErr = errors.New("X11 connection closed")
				}
				b.events <- endpointEvent{endpoint: display, err: connectionErr}
				return
			}
			b.events <- endpointEvent{endpoint: display, event: event}
		}
	}()
}

func (b *broker) handleEvent(received endpointEvent) {
	if request, ok := received.event.(xproto.SelectionRequestEvent); ok {
		b.clipboard.serve(received.endpoint, request)
		return
	}
	if b.clipboard.selectionChanged(received.endpoint, received.event) {
		b.clipboard.poll()
		return
	}
	if received.endpoint != b.nested {
		return
	}
	switch event := received.event.(type) {
	case xproto.MapRequestEvent:
		xproto.MapWindow(b.nested.connection, event.Window)
		b.fitWindow(event.Window)
		b.focusWindow(event.Window)
	case xproto.ConfigureRequestEvent:
		b.configureWindow(event)
	case xproto.ClientMessageEvent:
		if event.Type == b.nested.atoms.netWMActiveWindow {
			b.focusWindow(event.Window)
		} else {
			b.updateWindowState(event)
		}
	case xproto.MapNotifyEvent:
		b.fitWindow(event.Window)
		b.focusWindow(event.Window)
	case xproto.ConfigureNotifyEvent:
		if event.Window == b.nested.root && b.primary != 0 {
			b.fitWindow(b.primary)
		}
	}
}

func (b *broker) tick() bool {
	windows := b.applicationWindows()
	if len(windows) > 0 {
		b.seenWindow = true
		b.closeAt = time.Time{}
		b.primary = windows[0]
		b.fitWindow(b.primary)
		b.syncHostWindow()
	} else if b.seenWindow {
		if b.closeAt.IsZero() {
			b.closeAt = time.Now().Add(windowCloseGrace)
		} else if time.Now().After(b.closeAt) {
			return true
		}
	}
	b.clipboard.poll()
	return false
}

func (b *broker) applicationWindows() []xproto.Window {
	tree, err := xproto.QueryTree(b.nested.connection, b.nested.root).Reply()
	if err != nil {
		return nil
	}
	type candidate struct {
		window xproto.Window
		area   uint32
	}
	candidates := make([]candidate, 0, len(tree.Children))
	for _, window := range tree.Children {
		if window == b.nested.window || !b.isApplicationWindow(window) {
			continue
		}
		geometry, geometryErr := xproto.GetGeometry(b.nested.connection, xproto.Drawable(window)).Reply()
		if geometryErr != nil || geometry.Width < 32 || geometry.Height < 32 {
			continue
		}
		candidates = append(candidates, candidate{window: window, area: uint32(geometry.Width) * uint32(geometry.Height)})
	}
	for left := 0; left < len(candidates); left++ {
		for right := left + 1; right < len(candidates); right++ {
			if candidates[right].area > candidates[left].area {
				candidates[left], candidates[right] = candidates[right], candidates[left]
			}
		}
	}
	result := make([]xproto.Window, len(candidates))
	for index := range candidates {
		result[index] = candidates[index].window
	}
	return result
}

func (b *broker) isApplicationWindow(window xproto.Window) bool {
	attributes, err := xproto.GetWindowAttributes(b.nested.connection, window).Reply()
	if err != nil || attributes.MapState != xproto.MapStateViewable || attributes.OverrideRedirect || b.windowIsTransient(window) {
		return false
	}
	types := propertyAtoms(b.nested.connection, window, b.nested.atoms.netWMWindowType)
	if len(types) == 0 {
		return true
	}
	for _, kind := range types {
		if kind == b.nested.atoms.netWMWindowNormal || kind == b.nested.atoms.netWMWindowDialog {
			return true
		}
	}
	return false
}

func (b *broker) fitWindow(window xproto.Window) {
	if window == 0 || !b.windowCanFillRoot(window) {
		return
	}
	root, err := xproto.GetGeometry(b.nested.connection, xproto.Drawable(b.nested.root)).Reply()
	if err != nil || root.Width == 0 || root.Height == 0 {
		return
	}
	geometry, err := xproto.GetGeometry(b.nested.connection, xproto.Drawable(window)).Reply()
	if err == nil && geometry.X == 0 && geometry.Y == 0 && geometry.Width == root.Width && geometry.Height == root.Height && geometry.BorderWidth == 0 {
		return
	}
	_ = xproto.ConfigureWindowChecked(
		b.nested.connection,
		window,
		xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowBorderWidth,
		[]uint32{0, 0, uint32(root.Width), uint32(root.Height), 0},
	).Check()
}

func (b *broker) windowCanFillRoot(window xproto.Window) bool {
	attributes, err := xproto.GetWindowAttributes(b.nested.connection, window).Reply()
	if err != nil || attributes.OverrideRedirect || b.windowIsTransient(window) {
		return false
	}
	types := propertyAtoms(b.nested.connection, window, b.nested.atoms.netWMWindowType)
	return len(types) == 0 || containsAtom(types, b.nested.atoms.netWMWindowNormal)
}

func (b *broker) windowIsTransient(window xproto.Window) bool {
	reply, err := xproto.GetProperty(b.nested.connection, false, window, xproto.AtomWmTransientFor, xproto.AtomWindow, 0, 1).Reply()
	return err == nil && reply.Format == 32 && len(reply.Value) >= 4 && xproto.Window(xgb.Get32(reply.Value)) != xproto.WindowNone
}

func (b *broker) focusWindow(window xproto.Window) {
	attributes, err := xproto.GetWindowAttributes(b.nested.connection, window).Reply()
	if err != nil || attributes.MapState != xproto.MapStateViewable || attributes.OverrideRedirect {
		return
	}
	geometry, err := xproto.GetGeometry(b.nested.connection, xproto.Drawable(window)).Reply()
	if err != nil || geometry.Width < 32 || geometry.Height < 32 {
		return
	}
	types := propertyAtoms(b.nested.connection, window, b.nested.atoms.netWMWindowType)
	if len(types) > 0 && !containsAtom(types, b.nested.atoms.netWMWindowNormal) && !containsAtom(types, b.nested.atoms.netWMWindowDialog) {
		return
	}
	if err = xproto.SetInputFocusChecked(b.nested.connection, xproto.InputFocusPointerRoot, window, xproto.TimeCurrentTime).Check(); err != nil {
		return
	}
	setProperty32(b.nested.connection, b.nested.root, b.nested.atoms.netWMActiveWindow, xproto.AtomWindow, uint32(window))
}

func (b *broker) configureWindow(event xproto.ConfigureRequestEvent) {
	if b.windowCanFillRoot(event.Window) {
		b.fitWindow(event.Window)
		return
	}
	mask := uint16(0)
	values := []uint32{}
	appendValue := func(bit uint16, value uint32) {
		if event.ValueMask&bit != 0 {
			mask |= bit
			values = append(values, value)
		}
	}
	appendValue(xproto.ConfigWindowX, uint32(int32(event.X)))
	appendValue(xproto.ConfigWindowY, uint32(int32(event.Y)))
	appendValue(xproto.ConfigWindowWidth, uint32(event.Width))
	appendValue(xproto.ConfigWindowHeight, uint32(event.Height))
	appendValue(xproto.ConfigWindowBorderWidth, uint32(event.BorderWidth))
	appendValue(xproto.ConfigWindowSibling, uint32(event.Sibling))
	appendValue(xproto.ConfigWindowStackMode, uint32(event.StackMode))
	if mask != 0 {
		xproto.ConfigureWindow(b.nested.connection, event.Window, mask, values)
	}
}

func (b *broker) updateWindowState(event xproto.ClientMessageEvent) {
	if event.Type != b.nested.atoms.netWMState || event.Format != 32 || len(event.Data.Data32) < 3 {
		return
	}
	state := propertyAtoms(b.nested.connection, event.Window, b.nested.atoms.netWMState)
	for _, requested := range []xproto.Atom{xproto.Atom(event.Data.Data32[1]), xproto.Atom(event.Data.Data32[2])} {
		if requested == xproto.AtomNone {
			continue
		}
		state = applyState(state, requested, event.Data.Data32[0])
	}
	values := make([]uint32, len(state))
	for index, atom := range state {
		values[index] = uint32(atom)
	}
	setProperty32(b.nested.connection, event.Window, b.nested.atoms.netWMState, xproto.AtomAtom, values...)
}

func applyState(states []xproto.Atom, requested xproto.Atom, action uint32) []xproto.Atom {
	index := -1
	for current, state := range states {
		if state == requested {
			index = current
			break
		}
	}
	enabled := index >= 0
	want := action == 1 || action == 2 && !enabled
	if want == enabled {
		return states
	}
	if want {
		return append(states, requested)
	}
	return append(states[:index], states[index+1:]...)
}

func (b *broker) syncHostWindow() {
	if b.host == nil || b.options.HostWindow == "" || b.primary == 0 {
		return
	}
	if b.hostWindow == 0 {
		b.hostWindow = findClassWindow(b.host.connection, b.host.root, b.options.HostWindow)
		if b.hostWindow == 0 {
			return
		}
	}
	title := windowTitle(b.nested, b.primary)
	if title != "" && title != b.lastTitle {
		encoded := []byte(title)
		xproto.ChangeProperty(b.host.connection, xproto.PropModeReplace, b.hostWindow, b.host.atoms.netWMName, b.host.atoms.utf8, 8, uint32(len(encoded)), encoded)
		xproto.ChangeProperty(b.host.connection, xproto.PropModeReplace, b.hostWindow, xproto.AtomWmName, xproto.AtomString, 8, uint32(len(encoded)), encoded)
		b.lastTitle = title
	}
	icon := propertyBytes(b.nested.connection, b.primary, b.nested.atoms.netWMIcon, 1<<20)
	if len(icon) > 0 && !bytes.Equal(icon, b.lastIcon) {
		replaceProperty(b.host.connection, b.hostWindow, b.host.atoms.netWMIcon, xproto.AtomCardinal, 32, icon)
		b.lastIcon = append(b.lastIcon[:0], icon...)
	}
	states := propertyAtoms(b.nested.connection, b.primary, b.nested.atoms.netWMState)
	fullscreen := containsAtom(states, b.nested.atoms.netWMStateFullscreen)
	maximized := containsAtom(states, b.nested.atoms.netWMStateMaxHorz) && containsAtom(states, b.nested.atoms.netWMStateMaxVert)
	if fullscreen != b.fullscreen {
		requestHostState(b.host, b.hostWindow, b.host.atoms.netWMStateFullscreen, fullscreen)
		b.fullscreen = fullscreen
	}
	if maximized != b.maximized {
		requestHostState(b.host, b.hostWindow, b.host.atoms.netWMStateMaxHorz, maximized)
		requestHostState(b.host, b.hostWindow, b.host.atoms.netWMStateMaxVert, maximized)
		b.maximized = maximized
	}
	b.host.connection.Sync()
}

func requestHostState(host *endpoint, window xproto.Window, state xproto.Atom, enabled bool) {
	action := uint32(0)
	if enabled {
		action = 1
	}
	event := xproto.ClientMessageEvent{
		Format: 32,
		Window: window,
		Type:   host.atoms.netWMState,
		Data:   xproto.ClientMessageDataUnionData32New([]uint32{action, uint32(state), 0, 1, 0}),
	}
	xproto.SendEvent(host.connection, false, host.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(event.Bytes()))
}

func findClassWindow(connection *xgb.Conn, root xproto.Window, class string) xproto.Window {
	queue := []xproto.Window{root}
	seen := 0
	for len(queue) > 0 && seen < 4096 {
		window := queue[0]
		queue = queue[1:]
		seen++
		property, err := xproto.GetProperty(connection, false, window, xproto.AtomWmClass, xproto.AtomString, 0, 1024).Reply()
		if err == nil {
			for _, value := range bytes.Split(property.Value, []byte{0}) {
				if string(value) == class {
					return window
				}
			}
		}
		tree, err := xproto.QueryTree(connection, window).Reply()
		if err == nil {
			queue = append(queue, tree.Children...)
		}
	}
	return 0
}

func windowTitle(display *endpoint, window xproto.Window) string {
	for _, property := range []xproto.Atom{display.atoms.netWMName, xproto.AtomWmName} {
		reply, err := xproto.GetProperty(display.connection, false, window, property, xproto.GetPropertyTypeAny, 0, 1024).Reply()
		if err == nil && reply.Format == 8 && len(reply.Value) > 0 {
			return string(bytes.TrimRight(reply.Value, "\x00"))
		}
	}
	return ""
}

func propertyAtoms(connection *xgb.Conn, window xproto.Window, property xproto.Atom) []xproto.Atom {
	reply, err := xproto.GetProperty(connection, false, window, property, xproto.AtomAtom, 0, 1024).Reply()
	if err != nil || reply.Format != 32 || len(reply.Value)%4 != 0 {
		return nil
	}
	result := make([]xproto.Atom, len(reply.Value)/4)
	for index := range result {
		result[index] = xproto.Atom(xgb.Get32(reply.Value[index*4:]))
	}
	return result
}

func propertyBytes(connection *xgb.Conn, window xproto.Window, property xproto.Atom, limit uint32) []byte {
	reply, err := xproto.GetProperty(connection, false, window, property, xproto.GetPropertyTypeAny, 0, limit/4).Reply()
	if err != nil || reply.BytesAfter != 0 || uint32(len(reply.Value)) > limit {
		return nil
	}
	return reply.Value
}

func setProperty32(connection *xgb.Conn, window xproto.Window, property, propertyType xproto.Atom, values ...uint32) {
	data := make([]byte, len(values)*4)
	for index, value := range values {
		xgb.Put32(data[index*4:], value)
	}
	xproto.ChangeProperty(connection, xproto.PropModeReplace, window, property, propertyType, 32, uint32(len(values)), data)
}

func replaceProperty(connection *xgb.Conn, window xproto.Window, property, propertyType xproto.Atom, format byte, data []byte) {
	unit := int(format / 8)
	setup := xproto.Setup(connection)
	if unit == 0 || setup == nil || setup.MaximumRequestLength <= 6 {
		return
	}
	chunkSize := (int(setup.MaximumRequestLength) - 6) * 4
	chunkSize -= chunkSize % unit
	mode := byte(xproto.PropModeReplace)
	for offset := 0; offset < len(data); offset += chunkSize {
		end := offset + chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		xproto.ChangeProperty(connection, mode, window, property, propertyType, format, uint32(len(chunk)/unit), chunk)
		mode = xproto.PropModeAppend
	}
}

func containsAtom(atoms []xproto.Atom, wanted xproto.Atom) bool {
	for _, atom := range atoms {
		if atom == wanted {
			return true
		}
	}
	return false
}
