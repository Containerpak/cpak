/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestFlatpakApplicationOwnsItsID(t *testing.T) {
	app := types.Application{Flatpak: &types.FlatpakSource{Ref: "app/org.gnome.TextEditor/x86_64/stable"}}
	declared := types.DBusPolicy{Own: []string{"org.example.Helper"}}
	bus := flatpakSessionBus(app, declared)
	for _, name := range []string{"org.gnome.TextEditor", "org.gnome.TextEditor.SearchProvider", "org.example.Helper"} {
		if !bus.AllowsOwn(name) {
			t.Fatalf("%s is not ownable: %+v", name, bus.Own)
		}
	}
	for _, name := range []string{"org.gnome.TextEditorX", "org.gnome.Shell", "org.freedesktop.portal.Desktop"} {
		if bus.AllowsOwn(name) {
			t.Fatalf("%s must stay refused: %+v", name, bus.Own)
		}
	}
	if len(declared.Own) != 1 {
		t.Fatalf("the stored policy was changed: %+v", declared.Own)
	}
}

func TestNativeApplicationKeepsItsPolicy(t *testing.T) {
	bus := flatpakSessionBus(types.Application{Name: "org.example.App"}, types.DBusPolicy{})
	if bus.Enabled() {
		t.Fatalf("a native application gained bus names: %+v", bus.Own)
	}
}
