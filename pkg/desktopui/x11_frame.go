/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"strconv"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type desktopFrame struct {
	conn   *xgb.Conn
	root   xproto.Window
	window xproto.Window
}

func newDesktopFrame(title string) *desktopFrame {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	name, err := desktopAtom(conn, "_NET_WM_NAME")
	if err != nil {
		conn.Close()
		return nil
	}
	var window xproto.Window
	for attempt := 0; attempt < 50; attempt++ {
		window = desktopWindow(conn, root, name, title)
		if window != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if window == 0 {
		conn.Close()
		return nil
	}
	frame := &desktopFrame{conn: conn, root: root, window: window}
	frame.removeDecorations()
	conn.Sync()
	return frame
}

func (f *desktopFrame) Close() {
	f.conn.Close()
}

func (f *desktopFrame) StartMove() {
	pointer, err := xproto.QueryPointer(f.conn, f.root).Reply()
	if err != nil {
		return
	}
	move, err := desktopAtom(f.conn, "_NET_WM_MOVERESIZE")
	if err != nil {
		return
	}
	xproto.UngrabPointer(f.conn, xproto.TimeCurrentTime)
	event := xproto.ClientMessageEvent{
		Format: 32,
		Window: f.window,
		Type:   move,
		Data: xproto.ClientMessageDataUnionData32New([]uint32{
			uint32(int32(pointer.RootX)),
			uint32(int32(pointer.RootY)),
			8,
			1,
			1,
		}),
	}
	_ = xproto.SendEventChecked(
		f.conn,
		false,
		f.root,
		xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify,
		string(event.Bytes()),
	).Check()
}

func (f *desktopFrame) SetDialog(parent string) {
	windowType, err := desktopAtom(f.conn, "_NET_WM_WINDOW_TYPE")
	if err != nil {
		return
	}
	dialog, err := desktopAtom(f.conn, "_NET_WM_WINDOW_TYPE_DIALOG")
	if err != nil {
		return
	}
	desktopProperty32(f.conn, f.window, windowType, xproto.AtomAtom, uint32(dialog))

	state, err := desktopAtom(f.conn, "_NET_WM_STATE")
	if err == nil {
		modal, modalErr := desktopAtom(f.conn, "_NET_WM_STATE_MODAL")
		if modalErr == nil {
			desktopProperty32(f.conn, f.window, state, xproto.AtomAtom, uint32(modal))
		}
	}
	if parentWindow, ok := desktopParentWindow(parent); ok {
		desktopProperty32(f.conn, f.window, xproto.AtomWmTransientFor, xproto.AtomWindow, uint32(parentWindow))
	}
	f.conn.Sync()
}

func (f *desktopFrame) removeDecorations() {
	motif, err := desktopAtom(f.conn, "_MOTIF_WM_HINTS")
	if err != nil {
		return
	}
	hints := make([]byte, 20)
	xgb.Put32(hints[0:4], 2)
	xproto.ChangeProperty(f.conn, xproto.PropModeReplace, f.window, motif, motif, 32, 5, hints)
}

func desktopProperty32(conn *xgb.Conn, window xproto.Window, property, propertyType xproto.Atom, values ...uint32) {
	data := make([]byte, len(values)*4)
	for index, value := range values {
		xgb.Put32(data[index*4:], value)
	}
	xproto.ChangeProperty(conn, xproto.PropModeReplace, window, property, propertyType, 32, uint32(len(values)), data)
}

func desktopParentWindow(parent string) (xproto.Window, bool) {
	value, found := strings.CutPrefix(parent, "x11:")
	if !found || value == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(value, 16, 32)
	if err != nil || id == 0 {
		return 0, false
	}
	return xproto.Window(id), true
}

func desktopWindow(conn *xgb.Conn, root xproto.Window, name xproto.Atom, title string) xproto.Window {
	windows := []xproto.Window{root}
	for len(windows) > 0 {
		window := windows[0]
		windows = windows[1:]
		property, err := xproto.GetProperty(conn, false, window, name, xproto.GetPropertyTypeAny, 0, 256).Reply()
		if err == nil && string(property.Value) == title {
			return window
		}
		tree, err := xproto.QueryTree(conn, window).Reply()
		if err == nil {
			windows = append(windows, tree.Children...)
		}
	}
	return 0
}

func desktopAtom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Atom, nil
}
