/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
// Package trayproxy presents private XEmbed icons to the desktop's status notifier.
package trayproxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/composite"
	"github.com/jezek/xgb/xproto"
)

const iconSize = 32
const maxIcons = 32

var watcherNames = []string{"org.kde.StatusNotifierWatcher", "org.freedesktop.StatusNotifierWatcher"}

// Options keeps the private display separate from the host bus. The connection
// belongs to the proxy and is closed when Run returns.
type Options struct {
	Connection *xgb.Conn
	BusAddress string
	Name       string
}

type atoms struct {
	selection, opcode, manager, embed, info, orientation, visual xproto.Atom
	name, utf8                                                   xproto.Atom
}

type Proxy struct {
	options               Options
	x                     *xgb.Conn
	screen                *xproto.ScreenInfo
	owner                 xproto.Window
	atoms                 atoms
	items                 map[xproto.Window]*item
	active                atomic.Int64
	bus                   *dbus.Conn
	watcher, watcherOwner string
	lastBusCheck          time.Time
	nextID                uint64
}

// New claims only the tray selection on the supplied private display.
func New(options Options) (*Proxy, error) {
	if options.Connection == nil {
		return nil, errors.New("tray proxy needs a private X11 connection")
	}
	p := &Proxy{options: options, x: options.Connection, items: make(map[xproto.Window]*item)}
	var seed [8]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	p.nextID = binary.LittleEndian.Uint64(seed[:])
	p.screen = xproto.Setup(p.x).DefaultScreen(p.x)
	for name, target := range map[string]*xproto.Atom{
		fmt.Sprintf("_NET_SYSTEM_TRAY_S%d", p.x.DefaultScreen): &p.atoms.selection,
		"_NET_SYSTEM_TRAY_OPCODE":                              &p.atoms.opcode, "MANAGER": &p.atoms.manager,
		"_XEMBED": &p.atoms.embed, "_XEMBED_INFO": &p.atoms.info,
		"_NET_SYSTEM_TRAY_ORIENTATION": &p.atoms.orientation,
		"_NET_SYSTEM_TRAY_VISUAL":      &p.atoms.visual,
		"_NET_WM_NAME":                 &p.atoms.name, "UTF8_STRING": &p.atoms.utf8,
	} {
		reply, err := xproto.InternAtom(p.x, false, uint16(len(name)), name).Reply()
		if err != nil {
			return nil, err
		}
		*target = reply.Atom
	}
	selection, err := xproto.GetSelectionOwner(p.x, p.atoms.selection).Reply()
	if err != nil {
		return nil, err
	}
	if selection.Owner != 0 {
		return nil, errors.New("private display already has a tray manager")
	}
	if err := composite.Init(p.x); err != nil {
		return nil, err
	}
	if _, err := composite.QueryVersion(p.x, 0, 2).Reply(); err != nil {
		return nil, err
	}
	p.owner, err = xproto.NewWindowId(p.x)
	if err != nil {
		return nil, err
	}
	// Composite keeps each icon's full backing pixmap even though the parent
	// is one pixel outside the visible root. It must never become an app window.
	if err := xproto.CreateWindowChecked(p.x, p.screen.RootDepth, p.owner, p.screen.Root, -10000, -10000, 1, 1, 0,
		xproto.WindowClassInputOutput, p.screen.RootVisual,
		xproto.CwOverrideRedirect|xproto.CwEventMask, []uint32{1, xproto.EventMaskSubstructureNotify}).Check(); err != nil {
		return nil, err
	}
	writeProperty(p.x, p.owner, p.atoms.orientation, xproto.AtomCardinal, 0)
	writeProperty(p.x, p.owner, p.atoms.visual, xproto.AtomVisualid, uint32(p.trayVisual()))
	if err := xproto.MapWindowChecked(p.x, p.owner).Check(); err != nil {
		return nil, err
	}
	if err := xproto.SetSelectionOwnerChecked(p.x, p.owner, p.atoms.selection, xproto.TimeCurrentTime).Check(); err != nil {
		return nil, err
	}
	selection, err = xproto.GetSelectionOwner(p.x, p.atoms.selection).Reply()
	if err != nil || selection.Owner != p.owner {
		return nil, errors.New("could not own private tray selection")
	}
	message := xproto.ClientMessageEvent{Format: 32, Window: p.screen.Root, Type: p.atoms.manager,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{0, uint32(p.atoms.selection), uint32(p.owner), 0, 0})}
	if err := xproto.SendEventChecked(p.x, false, p.screen.Root, xproto.EventMaskStructureNotify, string(message.Bytes())).Check(); err != nil {
		return nil, err
	}
	return p, nil
}

// Active reports whether an application still has a docked icon.
func (p *Proxy) Active() bool { return p.active.Load() > 0 }

// Run follows icon and watcher changes until the private display closes.
// An absent host bus or watcher leaves the application running.
func (p *Proxy) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer p.x.Close()
	go func() { <-ctx.Done(); p.x.Close() }()
	defer func() {
		for _, icon := range p.items {
			p.remove(icon.window)
		}
		if p.bus != nil {
			p.bus.Close()
		}
	}()
	events := make(chan xgb.Event, 64)
	go func() {
		defer close(events)
		for {
			event, err := p.x.WaitForEvent()
			if err != nil {
				continue
			}
			if event == nil {
				return
			}
			select {
			case events <- event:
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			switch event := event.(type) {
			case xproto.ClientMessageEvent:
				if event.Type == p.atoms.opcode && event.Format == 32 && event.Data.Data32[1] == 0 {
					p.dock(xproto.Window(event.Data.Data32[2]))
				}
			case xproto.DestroyNotifyEvent:
				p.remove(event.Window)
			case xproto.ReparentNotifyEvent:
				if event.Parent != p.owner {
					p.remove(event.Window)
				}
			case xproto.SelectionClearEvent:
				if event.Selection == p.atoms.selection {
					return nil
				}
			case xproto.ConfigureNotifyEvent:
				if icon := p.items[event.Window]; icon != nil {
					icon.resetPixmap()
				}
			}
		case <-ticker.C:
			p.updateBus(ctx)
			for _, icon := range p.items {
				icon.update(ctx)
			}
		}
	}
}

func (p *Proxy) dock(window xproto.Window) {
	if window == 0 || window == p.owner || window == p.screen.Root || p.items[window] != nil || len(p.items) >= maxIcons {
		return
	}
	geometry, err := xproto.GetGeometry(p.x, xproto.Drawable(window)).Reply()
	if err != nil || geometry.Root != p.screen.Root {
		return
	}
	if err := xproto.ChangeWindowAttributesChecked(p.x, window, xproto.CwEventMask, []uint32{xproto.EventMaskStructureNotify | xproto.EventMaskPropertyChange}).Check(); err != nil {
		return
	}
	if err := composite.RedirectWindowChecked(p.x, window, composite.RedirectManual).Check(); err != nil {
		return
	}
	if err := xproto.ChangeSaveSetChecked(p.x, xproto.SetModeInsert, window).Check(); err != nil {
		return
	}
	if err := xproto.ReparentWindowChecked(p.x, window, p.owner, 0, 0).Check(); err != nil {
		return
	}
	_ = xproto.ConfigureWindowChecked(p.x, window, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{iconSize, iconSize}).Check()
	message := xproto.ClientMessageEvent{Format: 32, Window: window, Type: p.atoms.embed,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{0, 0, 0, uint32(p.owner), 0})}
	_ = xproto.SendEventChecked(p.x, false, window, 0, string(message.Bytes())).Check()
	_ = xproto.MapWindowChecked(p.x, window).Check()
	p.items[window] = &item{proxy: p, window: window}
	p.active.Store(int64(len(p.items)))
}

func (p *Proxy) remove(window xproto.Window) {
	icon := p.items[window]
	if icon == nil {
		return
	}
	if icon.bus != nil {
		icon.bus.Close()
	}
	icon.resetPixmap()
	delete(p.items, window)
	p.active.Store(int64(len(p.items)))
}

func (p *Proxy) updateBus(ctx context.Context) {
	if time.Since(p.lastBusCheck) < time.Second {
		return
	}
	p.lastBusCheck = time.Now()
	if p.bus == nil || !p.bus.Connected() {
		if p.bus != nil {
			p.bus.Close()
		}
		p.watcher, p.watcherOwner = "", ""
		if p.options.BusAddress == "" {
			return
		}
		connection, err := dbus.Connect(p.options.BusAddress, dbus.WithContext(ctx))
		if err != nil {
			p.bus = nil
			return
		}
		p.bus = connection
	}
	for _, name := range watcherNames {
		var owner string
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := p.bus.BusObject().CallWithContext(callCtx, "org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner)
		cancel()
		if err != nil || owner == "" {
			continue
		}
		if owner != p.watcherOwner || name != p.watcher {
			p.watcher, p.watcherOwner = name, owner
			for _, icon := range p.items {
				icon.registered = false
			}
		}
		return
	}
	p.watcher, p.watcherOwner = "", ""
}

func writeProperty(connection *xgb.Conn, window xproto.Window, property, kind xproto.Atom, values ...uint32) {
	data := make([]byte, 4*len(values))
	for i, value := range values {
		binary.LittleEndian.PutUint32(data[i*4:], value)
	}
	xproto.ChangeProperty(connection, xproto.PropModeReplace, window, property, kind, 32, uint32(len(values)), data)
}

func (p *Proxy) trayVisual() xproto.Visualid {
	for _, depth := range p.screen.AllowedDepths {
		if depth.Depth != 32 {
			continue
		}
		for _, visual := range depth.Visuals {
			if visual.Class == xproto.VisualClassTrueColor && visual.RedMask == 0xff0000 && visual.GreenMask == 0xff00 && visual.BlueMask == 0xff {
				return visual.VisualId
			}
		}
	}
	return p.screen.RootVisual
}
