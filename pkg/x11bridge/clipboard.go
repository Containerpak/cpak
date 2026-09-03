/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package x11bridge

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"
)

const (
	maxClipboardBytes     = 16 << 20
	maxClipboardTargets   = 64
	selectionTimeout      = 2 * time.Second
	selectionTransferMax  = 30 * time.Second
	selectionPoll         = 10 * time.Millisecond
	selectionChunkBytes   = 64 << 10
	directClipboardBytes  = 64 << 10
	maxClipboardTransfers = 4
)

type clipboardItem struct {
	target string
	kind   string
	format byte
	data   []byte
}

type clipboardData struct {
	items map[string]clipboardItem
}

type clipboardCapture struct {
	source     *endpoint
	dest       *endpoint
	selection  string
	owner      xproto.Window
	generation uint64
	data       *clipboardData
	err        error
}

type clipboardBridge struct {
	nested      *endpoint
	host        *endpoint
	hostToApp   bool
	appToHost   bool
	owners      map[string]xproto.Window
	generations map[string]uint64
	inflight    map[string]bool
	offers      map[*endpoint]map[xproto.Atom]*clipboardData
	results     chan clipboardCapture
	transfers   chan struct{}
}

func newClipboardBridge(nested, host *endpoint, hostToApp, appToHost bool) *clipboardBridge {
	bridge := &clipboardBridge{
		nested: nested, host: host, hostToApp: hostToApp, appToHost: appToHost,
		owners: make(map[string]xproto.Window), generations: make(map[string]uint64), inflight: make(map[string]bool),
		offers: make(map[*endpoint]map[xproto.Atom]*clipboardData), results: make(chan clipboardCapture, 8),
		transfers: make(chan struct{}, maxClipboardTransfers),
	}
	if hostToApp {
		bridge.watchSelectionChanges(host)
	}
	if appToHost {
		bridge.watchSelectionChanges(nested)
	}
	return bridge
}

func (b *clipboardBridge) poll() {
	if b == nil || b.host == nil {
		return
	}
	for {
		select {
		case result := <-b.results:
			key := clipboardKey(result.source, result.selection)
			b.inflight[key] = false
			if result.err != nil || result.data == nil || b.currentOwner(result.source, result.selection) != result.owner || b.generations[key] != result.generation {
				delete(b.owners, key)
				continue
			}
			selection := selectionAtom(result.dest, result.selection)
			if b.offers[result.dest] == nil {
				b.offers[result.dest] = make(map[xproto.Atom]*clipboardData)
			}
			b.offers[result.dest][selection] = result.data
			xproto.SetSelectionOwner(result.dest.connection, result.dest.window, selection, xproto.TimeCurrentTime)
			result.dest.connection.Sync()
		default:
			goto observed
		}
	}

observed:
	for _, selection := range []string{"CLIPBOARD", "PRIMARY"} {
		if b.hostToApp {
			b.observe(b.host, b.nested, selection)
		}
		if b.appToHost {
			b.observe(b.nested, b.host, selection)
		}
	}
}

func (b *clipboardBridge) watchSelectionChanges(display *endpoint) {
	if display == nil {
		return
	}
	if err := xfixes.Init(display.connection); err != nil {
		return
	}
	if _, err := xfixes.QueryVersion(display.connection, 5, 0).Reply(); err != nil {
		return
	}
	mask := uint32(xfixes.SelectionEventMaskSetSelectionOwner | xfixes.SelectionEventMaskSelectionWindowDestroy | xfixes.SelectionEventMaskSelectionClientClose)
	for _, selection := range []xproto.Atom{display.atoms.clipboard, display.atoms.primary} {
		if err := xfixes.SelectSelectionInputChecked(display.connection, display.window, selection, mask).Check(); err != nil {
			return
		}
	}
	display.connection.Sync()
}

func (b *clipboardBridge) selectionChanged(display *endpoint, event xgb.Event) bool {
	notification, ok := event.(xfixes.SelectionNotifyEvent)
	if !ok || display == nil {
		return false
	}
	selection := ""
	switch notification.Selection {
	case display.atoms.clipboard:
		selection = "CLIPBOARD"
	case display.atoms.primary:
		selection = "PRIMARY"
	default:
		return true
	}
	key := clipboardKey(display, selection)
	b.generations[key]++
	delete(b.owners, key)
	return true
}

func (b *clipboardBridge) observe(source, dest *endpoint, selection string) {
	key := clipboardKey(source, selection)
	owner := b.currentOwner(source, selection)
	if b.owners[key] == owner {
		return
	}
	b.owners[key] = owner
	if owner == xproto.WindowNone {
		b.clearOffer(dest, selection)
		return
	}
	if owner == source.window || b.inflight[key] {
		return
	}
	b.inflight[key] = true
	generation := b.generations[key]
	go func() {
		data, err := captureClipboard(source, selectionAtom(source, selection), owner)
		b.results <- clipboardCapture{source: source, dest: dest, selection: selection, owner: owner, generation: generation, data: data, err: err}
	}()
}

func (b *clipboardBridge) currentOwner(display *endpoint, selection string) xproto.Window {
	reply, err := xproto.GetSelectionOwner(display.connection, selectionAtom(display, selection)).Reply()
	if err != nil {
		return xproto.WindowNone
	}
	return reply.Owner
}

func (b *clipboardBridge) clearOffer(display *endpoint, selection string) {
	atom := selectionAtom(display, selection)
	if offers := b.offers[display]; offers != nil {
		delete(offers, atom)
	}
	if b.currentOwner(display, selection) == display.window {
		xproto.SetSelectionOwner(display.connection, xproto.WindowNone, atom, xproto.TimeCurrentTime)
	}
}

func (b *clipboardBridge) serve(display *endpoint, request xproto.SelectionRequestEvent) {
	if b == nil || display == nil || request.Owner != display.window {
		return
	}
	data := b.offers[display][request.Selection]
	if data == nil {
		sendSelectionNotify(display, request, xproto.AtomNone)
		return
	}
	property := request.Property
	if property == xproto.AtomNone {
		property = request.Target
	}
	if request.Target == display.atoms.targets {
		values := []uint32{uint32(display.atoms.targets)}
		for _, item := range data.items {
			atom, err := internAtom(display.connection, item.target)
			if err == nil {
				values = append(values, uint32(atom))
			}
		}
		setProperty32(display.connection, request.Requestor, property, xproto.AtomAtom, values...)
		sendSelectionNotify(display, request, property)
		return
	}
	target, err := atomName(display.connection, request.Target)
	if err != nil {
		sendSelectionNotify(display, request, xproto.AtomNone)
		return
	}
	item, ok := data.item(target)
	if !ok {
		sendSelectionNotify(display, request, xproto.AtomNone)
		return
	}
	kind, err := internAtom(display.connection, item.kind)
	if err != nil {
		sendSelectionNotify(display, request, xproto.AtomNone)
		return
	}
	if len(item.data) <= directClipboardBytes {
		xproto.ChangeProperty(display.connection, xproto.PropModeReplace, request.Requestor, property, kind, item.format, uint32(len(item.data))/uint32(item.format/8), item.data)
		sendSelectionNotify(display, request, property)
		return
	}
	if !b.beginTransfer() {
		sendSelectionNotify(display, request, xproto.AtomNone)
		return
	}
	total := make([]byte, 4)
	xgb.Put32(total, uint32(len(item.data)))
	xproto.ChangeProperty(display.connection, xproto.PropModeReplace, request.Requestor, property, display.atoms.incr, 32, 1, total)
	sendSelectionNotify(display, request, property)
	go func() {
		defer b.endTransfer()
		sendIncremental(display, request.Requestor, property, kind, item)
	}()
}

func (b *clipboardBridge) beginTransfer() bool {
	select {
	case b.transfers <- struct{}{}:
		return true
	default:
		return false
	}
}

func (b *clipboardBridge) endTransfer() {
	<-b.transfers
}

func captureClipboard(display *endpoint, selection xproto.Atom, owner xproto.Window) (*clipboardData, error) {
	display.selection.Lock()
	defer display.selection.Unlock()
	if current, err := xproto.GetSelectionOwner(display.connection, selection).Reply(); err != nil || current.Owner != owner {
		return nil, errors.New("clipboard owner changed")
	}
	targets, err := requestSelection(display, selection, display.atoms.targets)
	requested := []xproto.Atom{}
	if err == nil && targets.format == 32 {
		for offset := 0; offset+4 <= len(targets.data) && len(requested) < maxClipboardTargets; offset += 4 {
			requested = append(requested, xproto.Atom(xgb.Get32(targets.data[offset:])))
		}
	}
	if len(requested) == 0 {
		requested = append(requested, display.atoms.utf8, xproto.AtomString)
	}
	result := &clipboardData{items: make(map[string]clipboardItem)}
	total := 0
	plainTextCaptured := false
	for _, target := range requested {
		name, nameErr := atomName(display.connection, target)
		if nameErr != nil || !allowedClipboardTarget(name) || plainTextCaptured && plainClipboardTarget(name) {
			continue
		}
		value, readErr := requestSelection(display, selection, target)
		if readErr != nil || len(value.data) == 0 || len(value.data)+total > maxClipboardBytes {
			continue
		}
		kind, kindErr := atomName(display.connection, value.kind)
		if kindErr != nil || !validClipboardValue(value.format, value.data) {
			continue
		}
		result.items[name] = clipboardItem{target: name, kind: kind, format: value.format, data: append([]byte{}, value.data...)}
		total += len(value.data)
		if plainClipboardTarget(name) {
			plainTextCaptured = true
		}
	}
	if len(result.items) == 0 {
		return nil, errors.New("clipboard has no safe target")
	}
	if current, err := xproto.GetSelectionOwner(display.connection, selection).Reply(); err != nil || current.Owner != owner {
		return nil, errors.New("clipboard owner changed during transfer")
	}
	return result, nil
}

func (d *clipboardData) item(target string) (clipboardItem, bool) {
	item, ok := d.items[target]
	if ok && len(item.data) > 0 {
		return item, true
	}
	if !utf8ClipboardTarget(target) {
		return clipboardItem{}, false
	}
	for _, preferred := range []string{"UTF8_STRING", "text/plain;charset=utf-8", "text/plain;charset=UTF-8", "text/plain"} {
		for name, candidate := range d.items {
			if strings.EqualFold(name, preferred) && len(candidate.data) > 0 {
				candidate.target = target
				candidate.kind = target
				return candidate, true
			}
		}
	}
	return clipboardItem{}, false
}

func utf8ClipboardTarget(target string) bool {
	return strings.EqualFold(target, "UTF8_STRING") || strings.HasPrefix(strings.ToLower(target), "text/plain")
}

func validClipboardValue(format byte, data []byte) bool {
	switch format {
	case 8:
		return true
	case 16:
		return len(data)%2 == 0
	case 32:
		return len(data)%4 == 0
	default:
		return false
	}
}

func plainClipboardTarget(target string) bool {
	switch strings.ToLower(target) {
	case "utf8_string", "string", "text", "compound_text":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(target), "text/plain")
	}
}

type selectionValue struct {
	kind   xproto.Atom
	format byte
	data   []byte
}

func requestSelection(display *endpoint, selection, target xproto.Atom) (selectionValue, error) {
	propertyName := fmt.Sprintf("CPAK_SELECTION_%d", target)
	property, err := internAtom(display.connection, propertyName)
	if err != nil {
		return selectionValue{}, err
	}
	xproto.DeleteProperty(display.connection, display.window, property)
	xproto.ConvertSelection(display.connection, display.window, selection, target, property, xproto.TimeCurrentTime)
	display.connection.Sync()
	deadline := time.Now().Add(selectionTimeout)
	first, err := waitSelectionProperty(display, property, deadline)
	if err != nil {
		return selectionValue{}, err
	}
	if first.Type != display.atoms.incr {
		if first.BytesAfter != 0 || len(first.Value) > maxClipboardBytes {
			xproto.DeleteProperty(display.connection, display.window, property)
			return selectionValue{}, errors.New("clipboard value exceeds limit")
		}
		return selectionValue{kind: first.Type, format: first.Format, data: first.Value}, nil
	}
	if first.Format != 32 || len(first.Value) < 4 || xgb.Get32(first.Value) > maxClipboardBytes {
		return selectionValue{}, errors.New("clipboard incremental value exceeds limit")
	}
	value := selectionValue{}
	transferDeadline := time.Now().Add(selectionTransferMax)
	for {
		deadline = time.Now().Add(selectionTimeout)
		if deadline.After(transferDeadline) {
			deadline = transferDeadline
		}
		part, partErr := waitSelectionProperty(display, property, deadline)
		if partErr != nil {
			return selectionValue{}, partErr
		}
		if len(part.Value) == 0 {
			return value, nil
		}
		if value.kind == xproto.AtomNone {
			value.kind = part.Type
			value.format = part.Format
		}
		if part.Type != value.kind || part.Format != value.format || len(value.data)+len(part.Value) > maxClipboardBytes {
			return selectionValue{}, errors.New("invalid clipboard incremental value")
		}
		value.data = append(value.data, part.Value...)
	}
}

func waitSelectionProperty(display *endpoint, property xproto.Atom, deadline time.Time) (*xproto.GetPropertyReply, error) {
	for time.Now().Before(deadline) {
		reply, err := xproto.GetProperty(display.connection, true, display.window, property, xproto.GetPropertyTypeAny, 0, maxClipboardBytes/4+1).Reply()
		if err != nil {
			return nil, err
		}
		if reply.Type != xproto.AtomNone {
			return reply, nil
		}
		time.Sleep(selectionPoll)
	}
	return nil, errors.New("clipboard selection timed out")
}

func sendIncremental(display *endpoint, requestor xproto.Window, property, kind xproto.Atom, item clipboardItem) {
	transferDeadline := time.Now().Add(selectionTransferMax)
	if !waitPropertyMissing(display, requestor, property, clipboardDeadline(transferDeadline)) {
		return
	}
	unit := int(item.format / 8)
	chunk := selectionChunkBytes - selectionChunkBytes%unit
	for offset := 0; offset < len(item.data); offset += chunk {
		end := offset + chunk
		if end > len(item.data) {
			end = len(item.data)
		}
		part := item.data[offset:end]
		xproto.ChangeProperty(display.connection, xproto.PropModeReplace, requestor, property, kind, item.format, uint32(len(part)/unit), part)
		display.connection.Sync()
		if !waitPropertyMissing(display, requestor, property, clipboardDeadline(transferDeadline)) {
			return
		}
	}
	xproto.ChangeProperty(display.connection, xproto.PropModeReplace, requestor, property, kind, item.format, 0, nil)
	display.connection.Sync()
}

func clipboardDeadline(transferDeadline time.Time) time.Time {
	deadline := time.Now().Add(selectionTimeout)
	if deadline.After(transferDeadline) {
		return transferDeadline
	}
	return deadline
}

func waitPropertyMissing(display *endpoint, window xproto.Window, property xproto.Atom, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		reply, err := xproto.GetProperty(display.connection, false, window, property, xproto.GetPropertyTypeAny, 0, 0).Reply()
		if err != nil {
			return false
		}
		if reply.Type == xproto.AtomNone {
			return true
		}
		time.Sleep(selectionPoll)
	}
	return false
}

func sendSelectionNotify(display *endpoint, request xproto.SelectionRequestEvent, property xproto.Atom) {
	event := xproto.SelectionNotifyEvent{
		Time: request.Time, Requestor: request.Requestor, Selection: request.Selection, Target: request.Target, Property: property,
	}
	xproto.SendEvent(display.connection, false, request.Requestor, xproto.EventMaskNoEvent, string(event.Bytes()))
	display.connection.Sync()
}

func allowedClipboardTarget(name string) bool {
	normalized := strings.ToLower(name)
	if strings.Contains(normalized, "uri") || strings.Contains(normalized, "file") || strings.Contains(normalized, "copied-files") {
		return false
	}
	if strings.HasPrefix(normalized, "text/plain") {
		return true
	}
	switch normalized {
	case "utf8_string", "string", "text", "compound_text", "text/html",
		"image/png", "image/jpeg", "image/bmp", "image/gif", "image/tiff", "image/webp":
		return true
	default:
		return false
	}
}

func selectionAtom(display *endpoint, name string) xproto.Atom {
	if name == "PRIMARY" {
		return display.atoms.primary
	}
	return display.atoms.clipboard
}

func clipboardKey(display *endpoint, selection string) string {
	return fmt.Sprintf("%p:%s", display, selection)
}

func internAtom(connection *xgb.Conn, name string) (xproto.Atom, error) {
	reply, err := xproto.InternAtom(connection, false, uint16(len(name)), name).Reply()
	if err != nil {
		return xproto.AtomNone, err
	}
	return reply.Atom, nil
}

func atomName(connection *xgb.Conn, atom xproto.Atom) (string, error) {
	reply, err := xproto.GetAtomName(connection, atom).Reply()
	if err != nil {
		return "", err
	}
	return reply.Name, nil
}
