/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopbus

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestUpdateMonitorPathUsesTheCallerAndToken(t *testing.T) {
	call := &dbus.Message{Body: []any{map[string]dbus.Variant{"handle_token": dbus.MakeVariant("tdesktop7")}}}
	if got := updateMonitorPath(":1.42", call); got != "/org/freedesktop/portal/Flatpak/update_monitor/1_42/tdesktop7" {
		t.Fatalf("path %q", got)
	}
	bad := &dbus.Message{Body: []any{map[string]dbus.Variant{"handle_token": dbus.MakeVariant("../x")}}}
	if got := updateMonitorPath(":1.42", bad); got != "/org/freedesktop/portal/Flatpak/update_monitor/1_42/cpak" {
		t.Fatalf("path %q", got)
	}
	if got := updateMonitorPath("", &dbus.Message{}); !got.IsValid() {
		t.Fatalf("invalid object path %q", got)
	}
}
