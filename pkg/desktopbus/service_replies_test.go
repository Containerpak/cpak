/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"context"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestServiceRepliesKeepEachCaller(t *testing.T) {
	state := &connectionState{serviceReplies: map[serviceReplyKey]struct{}{}}
	state.allowServiceReply(7, ":1.1")
	state.allowServiceReply(7, ":1.2")
	if state.takeServiceReply(7, ":1.3") {
		t.Fatal("an unrelated destination consumed a reply")
	}
	for _, sender := range []string{":1.2", ":1.1"} {
		if !state.takeServiceReply(7, sender) {
			t.Fatalf("caller %s lost its reply to another caller using the same serial", sender)
		}
		if state.takeServiceReply(7, sender) {
			t.Fatal("accepted a second reply to the same call")
		}
	}
}

func TestServiceCallsWithoutRepliesDoNotAccumulate(t *testing.T) {
	proxy := &Proxy{options: Options{Policy: types.DBusPolicy{Own: []string{"org.example.Service"}}}}
	state := &connectionState{uniqueName: ":1.42", serviceReplies: map[serviceReplyKey]struct{}{}}
	call := &dbus.Message{Type: dbus.TypeMethodCall, Flags: dbus.FlagNoReplyExpected, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldSender: dbus.MakeVariant(":1.7"), dbus.FieldDestination: dbus.MakeVariant(":1.42"),
	}}
	for i := 0; i < 1000; i++ {
		if !proxy.upstreamMessageAllowed(state, call) {
			t.Fatal("a no-reply call to a declared service was dropped")
		}
	}
	if len(state.serviceReplies) != 0 {
		t.Fatal("no-reply calls left pending replies behind")
	}
}

func TestServiceSignalsRespectDestinationPolicy(t *testing.T) {
	proxy := &Proxy{options: Options{Policy: types.DBusPolicy{
		Own:  []string{"org.example.Service"},
		Talk: []types.DBusCallGrant{{Name: "org.example.Watcher", Path: "/Watcher", Interface: "org.example.Watcher", Members: []string{"Register"}}},
	}}}
	for destination, allowed := range map[string]bool{"": true, "org.example.Watcher": true, "org.example.Unrelated": false, ":1.7": false} {
		message := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{}}
		if destination != "" {
			message.Headers[dbus.FieldDestination] = dbus.MakeVariant(destination)
		}
		if got := !proxy.intercept(context.Background(), nil, nil, message); got != allowed {
			t.Errorf("signal to %q: allowed %v, want %v", destination, got, allowed)
		}
	}
	if policyBusCallAllowed(proxy.options.Policy, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "ReleaseName", []any{"org.example.Watcher"}) {
		t.Fatal("a talk grant allowed releasing a name")
	}
}

func TestServiceReplyTrackingIsBounded(t *testing.T) {
	state := &connectionState{}
	for serial := uint32(1); serial <= maxServiceReplies; serial++ {
		if !state.allowServiceReply(serial, ":1.1") {
			t.Fatal("a call within the pending reply limit was refused")
		}
	}
	if state.allowServiceReply(maxServiceReplies+1, ":1.1") {
		t.Fatal("pending replies exceeded the per-connection limit")
	}
	if !state.takeServiceReply(1, ":1.1") || !state.allowServiceReply(maxServiceReplies+1, ":1.1") {
		t.Fatal("a completed call did not free its pending reply slot")
	}
}
