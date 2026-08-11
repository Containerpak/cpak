/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package main

import (
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

type x11Frame struct {
	conn   *xgb.Conn
	root   xproto.Window
	window xproto.Window
}

func newX11Frame(title string) *x11Frame {
	conn, err := xgb.NewConn()
	if err != nil {
		return nil
	}
	root := xproto.Setup(conn).DefaultScreen(conn).Root
	name, err := internAtom(conn, "_NET_WM_NAME")
	if err != nil {
		conn.Close()
		return nil
	}

	var window xproto.Window
	for attempt := 0; attempt < 50; attempt++ {
		window = findX11Window(conn, root, name, title)
		if window != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if window == 0 {
		conn.Close()
		return nil
	}

	frame := &x11Frame{conn: conn, root: root, window: window}
	frame.removeDecorations()
	conn.Sync()
	return frame
}

func (f *x11Frame) Close() {
	f.conn.Close()
}

func (f *x11Frame) StartMove() {
	pointer, err := xproto.QueryPointer(f.conn, f.root).Reply()
	if err != nil {
		return
	}
	move, err := internAtom(f.conn, "_NET_WM_MOVERESIZE")
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

func (f *x11Frame) removeDecorations() {
	motif, err := internAtom(f.conn, "_MOTIF_WM_HINTS")
	if err != nil {
		return
	}
	hints := make([]byte, 20)
	xgb.Put32(hints[0:4], 2)
	xproto.ChangeProperty(f.conn, xproto.PropModeReplace, f.window, motif, motif, 32, 5, hints)
}

func findX11Window(conn *xgb.Conn, root xproto.Window, name xproto.Atom, title string) xproto.Window {
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

func internAtom(conn *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	return reply.Atom, nil
}
