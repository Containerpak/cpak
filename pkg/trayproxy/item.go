/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package trayproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"github.com/jezek/xgb/composite"
	"github.com/jezek/xgb/xproto"
)

var itemInterfaces = []string{"org.kde.StatusNotifierItem", "org.freedesktop.StatusNotifierItem"}

const itemPath = dbus.ObjectPath("/StatusNotifierItem")

type pixmap struct {
	Width, Height int32
	Data          []byte
}

type tooltip struct {
	IconName           string
	IconPixmap         []pixmap
	Title, Description string
}

type item struct {
	proxy         *Proxy
	window        xproto.Window
	pixmap        xproto.Pixmap
	bus           *dbus.Conn
	properties    *prop.Properties
	serviceName   string
	registered    bool
	pixels        []byte
	title, status string
}

func (i *item) resetPixmap() {
	if i.pixmap != 0 {
		xproto.FreePixmap(i.proxy.x, i.pixmap)
		i.pixmap = 0
	}
}

func (i *item) update(ctx context.Context) {
	p := i.proxy
	attrs, err := xproto.GetWindowAttributes(p.x, i.window).Reply()
	if err != nil {
		p.remove(i.window)
		return
	}
	mapped := true
	if info, err := xproto.GetProperty(p.x, false, i.window, p.atoms.info, p.atoms.info, 0, 2).Reply(); err == nil && info.Format == 32 && len(info.Value) >= 8 {
		mapped = binary.LittleEndian.Uint32(info.Value[4:])&1 != 0
	}
	if mapped && attrs.MapState == xproto.MapStateUnmapped {
		if xproto.MapWindowChecked(p.x, i.window).Check() != nil {
			return
		}
		i.resetPixmap()
	} else if !mapped && attrs.MapState != xproto.MapStateUnmapped {
		_ = xproto.UnmapWindowChecked(p.x, i.window).Check()
		i.resetPixmap()
	}
	status := "Active"
	if !mapped {
		status = "Passive"
	}
	title := strings.TrimSpace(strings.ToValidUTF8(p.options.Name, ""))
	if len(title) > 256 {
		title = strings.ToValidUTF8(title[:256], "")
	}
	for _, property := range []xproto.Atom{p.atoms.name, xproto.AtomWmName} {
		if name, err := xproto.GetProperty(p.x, false, i.window, property, xproto.GetPropertyTypeAny, 0, 64).Reply(); err == nil && name.Format == 8 && len(name.Value) > 0 {
			title = strings.TrimSpace(strings.ToValidUTF8(string(name.Value), ""))
			break
		}
	}
	title = strings.ReplaceAll(strings.ToValidUTF8(title, ""), "\x00", "")
	changedTitle, changedStatus := title != i.title, status != i.status
	i.title, i.status = title, status
	var pixels []byte
	if mapped {
		pixels = i.capture()
	}
	changedIcon := len(pixels) > 0 && !bytes.Equal(pixels, i.pixels)
	if changedIcon {
		i.pixels = pixels
	}
	if i.bus != nil && !i.bus.Connected() {
		i.bus.Close()
		i.bus = nil
		i.properties = nil
		i.registered = false
	}
	if p.watcher == "" {
		return
	}
	if i.bus == nil && !i.publish(ctx) {
		return
	}
	if changedTitle {
		i.set("Title", title, "NewTitle")
	}
	if changedStatus {
		i.set("Status", status, "NewStatus", status)
	}
	if changedIcon {
		i.set("IconPixmap", i.iconPixmaps(), "NewIcon")
	}
	if changedIcon || changedTitle {
		i.set("ToolTip", tooltip{Title: i.title, IconPixmap: i.iconPixmaps()}, "NewToolTip")
	}
	if !i.registered {
		callCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := i.bus.Object(p.watcher, "/StatusNotifierWatcher").CallWithContext(callCtx, p.watcher+".RegisterStatusNotifierItem", 0, i.serviceName).Err
		cancel()
		i.registered = err == nil
	}
}

func (i *item) publish(ctx context.Context) bool {
	connection, err := dbus.Connect(i.proxy.options.BusAddress, dbus.WithContext(ctx))
	if err != nil {
		return false
	}
	if i.serviceName == "" {
		i.proxy.nextID++
		i.serviceName = fmt.Sprintf("org.kde.StatusNotifierItem-%d-%d", os.Getpid(), i.proxy.nextID)
	}
	if reply, err := connection.RequestName(i.serviceName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		connection.Close()
		return false
	}
	properties := prop.Map{}
	for _, iface := range itemInterfaces {
		if err := connection.Export(i, itemPath, iface); err != nil {
			connection.Close()
			return false
		}
		properties[iface] = map[string]*prop.Prop{
			"Category": {Value: "ApplicationStatus"}, "Id": {Value: i.serviceName}, "Title": {Value: i.title}, "Status": {Value: i.status},
			"WindowId": {Value: uint32(0)}, "IconName": {Value: ""}, "IconPixmap": {Value: i.iconPixmaps()},
			"OverlayIconName": {Value: ""}, "OverlayIconPixmap": {Value: []pixmap{}},
			"AttentionIconName": {Value: ""}, "AttentionIconPixmap": {Value: []pixmap{}}, "AttentionMovieName": {Value: ""},
			"ToolTip": {Value: tooltip{Title: i.title, IconPixmap: i.iconPixmaps()}}, "ItemIsMenu": {Value: false},
		}
	}
	props, err := prop.Export(connection, itemPath, properties)
	if err != nil {
		connection.Close()
		return false
	}
	node := &introspect.Node{Interfaces: []introspect.Interface{introspect.IntrospectData, prop.IntrospectData}}
	for _, iface := range itemInterfaces {
		node.Interfaces = append(node.Interfaces, introspect.Interface{Name: iface, Methods: introspect.Methods(i), Properties: props.Introspection(iface), Signals: []introspect.Signal{
			{Name: "NewTitle"}, {Name: "NewIcon"}, {Name: "NewToolTip"}, {Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}},
		}})
	}
	if err := connection.Export(introspect.NewIntrospectable(node), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		connection.Close()
		return false
	}
	i.bus, i.properties = connection, props
	return true
}

func (i *item) iconPixmaps() []pixmap {
	if len(i.pixels) == 0 {
		return []pixmap{}
	}
	return []pixmap{{Width: iconSize, Height: iconSize, Data: i.pixels}}
}

func (i *item) set(property string, value any, signal string, body ...any) {
	for _, iface := range itemInterfaces {
		i.properties.SetMust(iface, property, value)
		_ = i.bus.Emit(itemPath, iface+"."+signal, body...)
	}
}

func (i *item) capture() []byte {
	p := i.proxy
	if i.pixmap == 0 {
		pixmap, err := xproto.NewPixmapId(p.x)
		if err != nil {
			return nil
		}
		if err := composite.NameWindowPixmapChecked(p.x, i.window, pixmap).Check(); err != nil {
			return nil
		}
		i.pixmap = pixmap
	}
	image, err := xproto.GetImage(p.x, xproto.ImageFormatZPixmap, xproto.Drawable(i.pixmap), 0, 0, iconSize, iconSize, 0xffffffff).Reply()
	if err != nil {
		i.resetPixmap()
		return nil
	}
	var stride int
	for _, format := range xproto.Setup(p.x).PixmapFormats {
		if format.Depth == image.Depth {
			stride = int(format.BitsPerPixel) / 8
			break
		}
	}
	if stride < 2 || stride > 4 || len(image.Data) < iconSize*iconSize*stride {
		return nil
	}
	visual := p.screen.RootVisual
	if attrs, err := xproto.GetWindowAttributes(p.x, i.window).Reply(); err == nil {
		visual = attrs.Visual
	}
	red, green, blue := uint32(0xff0000), uint32(0xff00), uint32(0xff)
	for _, depth := range p.screen.AllowedDepths {
		for _, v := range depth.Visuals {
			if v.VisualId == visual {
				red, green, blue = v.RedMask, v.GreenMask, v.BlueMask
			}
		}
	}
	pixels := make([]byte, iconSize*iconSize*4)
	for n := 0; n < iconSize*iconSize; n++ {
		var value uint32
		for b := 0; b < stride; b++ {
			shift := b * 8
			if xproto.Setup(p.x).ImageByteOrder == xproto.ImageOrderMSBFirst {
				shift = (stride - 1 - b) * 8
			}
			value |= uint32(image.Data[n*stride+b]) << shift
		}
		alpha := byte(255)
		if image.Depth == 32 {
			alpha = byte(value >> 24)
		}
		pixels[n*4], pixels[n*4+1], pixels[n*4+2], pixels[n*4+3] = alpha, colorByte(value, red), colorByte(value, green), colorByte(value, blue)
	}
	return pixels
}

func colorByte(value, mask uint32) byte {
	if mask == 0 {
		return 0
	}
	shift := bits.TrailingZeros32(mask)
	return byte(uint64((value&mask)>>shift) * 255 / uint64(mask>>shift))
}

func (i *item) click(button byte, x, y int32) *dbus.Error {
	event := xproto.ButtonPressEvent{Detail: xproto.Button(button), Time: xproto.TimeCurrentTime, Root: i.proxy.screen.Root, Event: i.window,
		RootX: int16(max(-32768, min(32767, x))), RootY: int16(max(-32768, min(32767, y))), EventX: iconSize / 2, EventY: iconSize / 2, SameScreen: true}
	if err := xproto.SendEventChecked(i.proxy.x, false, i.window, xproto.EventMaskButtonPress, string(event.Bytes())).Check(); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := xproto.SendEventChecked(i.proxy.x, false, i.window, xproto.EventMaskButtonRelease, string(xproto.ButtonReleaseEvent(event).Bytes())).Check(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (i *item) Activate(x, y int32) *dbus.Error          { return i.click(1, x, y) }
func (i *item) SecondaryActivate(x, y int32) *dbus.Error { return i.click(2, x, y) }
func (i *item) ContextMenu(x, y int32) *dbus.Error       { return i.click(3, x, y) }
func (i *item) Scroll(delta int32, orientation string) *dbus.Error {
	if orientation != "vertical" && orientation != "horizontal" {
		return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"unknown scroll orientation"})
	}
	if delta == 0 {
		return nil
	}
	button := byte(4)
	if orientation == "horizontal" {
		button = 6
	}
	if delta < 0 {
		button++
	}
	distance := int64(delta)
	if distance < 0 {
		distance = -distance
	}
	for n := int64(0); n < min(10, max(1, distance/120)); n++ {
		if err := i.click(button, 0, 0); err != nil {
			return err
		}
	}
	return nil
}
