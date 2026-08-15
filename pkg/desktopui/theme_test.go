/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"image/color"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestAppearanceAccent(t *testing.T) {
	accent, ok := appearanceAccent(dbus.MakeVariant([]float64{0.2, 0.4, 0.8}))
	if !ok || accent != (color.RGBA{R: 51, G: 102, B: 204, A: 255}) {
		t.Fatalf("accent: %#v, %t", accent, ok)
	}
	if _, ok = appearanceAccent([]float64{1.2, 0.4, 0.8}); ok {
		t.Fatal("out-of-range accent was accepted")
	}
}

func TestPaletteForAppearance(t *testing.T) {
	accent := color.RGBA{R: 0xf0, G: 0xc0, B: 0x30, A: 0xff}
	dark := paletteForAppearance(true, accent)
	light := paletteForAppearance(false, accent)
	if dark.background == light.background || dark.text == light.text {
		t.Fatal("light and dark palettes are identical")
	}
	if dark.accent != accent || light.accent != accent {
		t.Fatal("desktop accent was not preserved")
	}
	if dark.onAccent.R != 0x0a || light.onAccent.R != 0x0a {
		t.Fatal("bright accent did not get a dark foreground")
	}
}

func TestDialogActionColorsReserveAccentForRecommendedActions(t *testing.T) {
	style := dialogStyleFromPalette(paletteForAppearance(true, color.RGBA{R: 0x47, G: 0x74, B: 0xff, A: 0xff}))
	background, foreground := style.ActionColors(false, false, true)
	if background != style.Button || foreground != style.OnButton {
		t.Fatalf("neutral action colors: %#v %#v", background, foreground)
	}
	background, foreground = style.ActionColors(true, false, true)
	if background != style.Accent || foreground != style.OnAccent {
		t.Fatalf("recommended action colors: %#v %#v", background, foreground)
	}
	background, foreground = style.ActionColors(true, false, false)
	if background != style.SurfaceHover || foreground != style.Muted {
		t.Fatalf("disabled action colors: %#v %#v", background, foreground)
	}
}
