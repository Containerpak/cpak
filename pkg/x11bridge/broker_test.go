/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package x11bridge

import (
	"reflect"
	"testing"

	"github.com/jezek/xgb/xproto"
)

func TestClipboardTargetsExcludeHostFileTransfers(t *testing.T) {
	for _, target := range []string{"text/uri-list", "x-special/gnome-copied-files", "application/x-file-list", "FILE_NAME"} {
		if allowedClipboardTarget(target) {
			t.Fatalf("host file target %q was allowed", target)
		}
	}
	for _, target := range []string{"UTF8_STRING", "text/plain;charset=utf-8", "text/html", "image/png", "image/webp"} {
		if !allowedClipboardTarget(target) {
			t.Fatalf("safe clipboard target %q was refused", target)
		}
	}
}

func TestClipboardValueMatchesItsX11Format(t *testing.T) {
	for _, test := range []struct {
		format byte
		data   []byte
		valid  bool
	}{
		{format: 8, data: []byte{1}, valid: true},
		{format: 16, data: []byte{1, 2}, valid: true},
		{format: 16, data: []byte{1}, valid: false},
		{format: 32, data: []byte{1, 2, 3, 4}, valid: true},
		{format: 32, data: []byte{1, 2, 3}, valid: false},
		{format: 64, data: []byte{1, 2, 3, 4}, valid: false},
	} {
		if validClipboardValue(test.format, test.data) != test.valid {
			t.Fatalf("format %d with %d bytes: expected valid=%t", test.format, len(test.data), test.valid)
		}
	}
}

func TestClipboardPlainTextTargets(t *testing.T) {
	for _, target := range []string{"UTF8_STRING", "STRING", "TEXT", "COMPOUND_TEXT", "text/plain", "text/plain;charset=utf-8"} {
		if !plainClipboardTarget(target) {
			t.Fatalf("plain text target %q was not recognized", target)
		}
	}
	for _, target := range []string{"text/html", "image/png"} {
		if plainClipboardTarget(target) {
			t.Fatalf("non-plain target %q was recognized as plain text", target)
		}
	}
}

func TestClipboardUsesReadyUTF8Alias(t *testing.T) {
	data := &clipboardData{items: map[string]clipboardItem{
		"UTF8_STRING":              {target: "UTF8_STRING", kind: "UTF8_STRING"},
		"text/plain;charset=utf-8": {target: "text/plain;charset=utf-8", kind: "text/plain;charset=utf-8", format: 8, data: []byte("ready")},
	}}
	item, ok := data.item("UTF8_STRING")
	if !ok || string(item.data) != "ready" || item.target != "UTF8_STRING" || item.kind != "UTF8_STRING" {
		t.Fatalf("UTF-8 fallback: %+v, available=%t", item, ok)
	}
	if _, ok = data.item("image/png"); ok {
		t.Fatal("text was offered for an image target")
	}
}

func TestClipboardLimitsConcurrentIncrementalTransfers(t *testing.T) {
	bridge := newClipboardBridge(nil, nil, false, false)
	for transfer := 0; transfer < maxClipboardTransfers; transfer++ {
		if !bridge.beginTransfer() {
			t.Fatalf("transfer %d was refused", transfer)
		}
	}
	if bridge.beginTransfer() {
		t.Fatal("transfer above the concurrency limit was accepted")
	}
	bridge.endTransfer()
	if !bridge.beginTransfer() {
		t.Fatal("released transfer slot was not reused")
	}
}

func TestWindowStateActionsAreAppliedExactly(t *testing.T) {
	first := xproto.Atom(10)
	second := xproto.Atom(20)
	states := applyState(nil, first, 1)
	if !reflect.DeepEqual(states, []xproto.Atom{first}) {
		t.Fatalf("add state: %v", states)
	}
	states = applyState(states, first, 1)
	if !reflect.DeepEqual(states, []xproto.Atom{first}) {
		t.Fatalf("duplicate state: %v", states)
	}
	states = applyState(states, second, 2)
	if !reflect.DeepEqual(states, []xproto.Atom{first, second}) {
		t.Fatalf("toggle state on: %v", states)
	}
	states = applyState(states, first, 0)
	if !reflect.DeepEqual(states, []xproto.Atom{second}) {
		t.Fatalf("remove state: %v", states)
	}
	states = applyState(states, second, 2)
	if len(states) != 0 {
		t.Fatalf("toggle state off: %v", states)
	}
}
