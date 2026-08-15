/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
	"image/color"
	"math"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	appearanceNamespace = "org.freedesktop.appearance"
	settingsInterface   = "org.freedesktop.portal.Settings"
	settingsReadOne     = settingsInterface + ".ReadOne"
)

type desktopPalette struct {
	dark          bool
	background    color.RGBA
	surface       color.RGBA
	surfaceStrong color.RGBA
	line          color.RGBA
	text          color.RGBA
	muted         color.RGBA
	controlMuted  color.RGBA
	button        color.RGBA
	buttonHot     color.RGBA
	onButton      color.RGBA
	accent        color.RGBA
	accentHot     color.RGBA
	onAccent      color.RGBA
	secondary     color.RGBA
	positive      color.RGBA
}

func currentDesktopPalette() desktopPalette {
	dark, accent := fallbackAppearance()
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	if scheme, portalAccent, ok := portalAppearance(ctx); ok {
		dark = scheme
		accent = portalAccent
	}
	return paletteForAppearance(dark, accent)
}

func portalAppearance(ctx context.Context) (bool, color.RGBA, bool) {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return false, color.RGBA{}, false
	}
	defer connection.Close()
	object := connection.Object("org.freedesktop.portal.Desktop", dbus.ObjectPath("/org/freedesktop/portal/desktop"))
	dark, rgb := fallbackAppearance()
	found := false
	if scheme, readErr := readAppearanceSetting(ctx, object, "color-scheme"); readErr == nil {
		if portalDark, ok := appearanceDark(scheme); ok {
			dark = portalDark
			found = true
		}
	}
	if accent, readErr := readAppearanceSetting(ctx, object, "accent-color"); readErr == nil {
		if portalAccent, ok := appearanceAccent(accent); ok {
			rgb = portalAccent
			found = true
		}
	}
	return dark, rgb, found
}

func readAppearanceSetting(ctx context.Context, object dbus.BusObject, key string) (any, error) {
	var value dbus.Variant
	if err := object.CallWithContext(ctx, settingsReadOne, 0, appearanceNamespace, key).Store(&value); err != nil {
		return nil, err
	}
	return unwrapVariant(value), nil
}

func unwrapVariant(value any) any {
	for {
		variant, ok := value.(dbus.Variant)
		if !ok {
			return value
		}
		value = variant.Value()
	}
}

func appearanceDark(value any) (bool, bool) {
	switch value := unwrapVariant(value).(type) {
	case uint32:
		if value == 1 {
			return true, true
		}
		if value == 2 {
			return false, true
		}
	case int32:
		return appearanceDark(uint32(value))
	}
	return false, false
}

func appearanceAccent(value any) (color.RGBA, bool) {
	value = unwrapVariant(value)
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Struct {
		return color.RGBA{}, false
	}
	var length int
	if reflected.Kind() == reflect.Struct {
		length = reflected.NumField()
	} else {
		length = reflected.Len()
	}
	components := make([]float64, 0, 3)
	for index := 0; index < length && len(components) < 3; index++ {
		component := reflected.Index(index)
		if reflected.Kind() == reflect.Struct {
			component = reflected.Field(index)
		}
		if component.Kind() == reflect.Interface {
			component = component.Elem()
		}
		if component.Kind() != reflect.Float64 {
			return color.RGBA{}, false
		}
		components = append(components, component.Float())
	}
	if len(components) != 3 {
		return color.RGBA{}, false
	}
	for _, component := range components {
		if component < 0 || component > 1 || math.IsNaN(component) || math.IsInf(component, 0) {
			return color.RGBA{}, false
		}
	}
	return color.RGBA{
		R: uint8(math.Round(components[0] * 255)),
		G: uint8(math.Round(components[1] * 255)),
		B: uint8(math.Round(components[2] * 255)),
		A: 0xff,
	}, true
}

func fallbackAppearance() (bool, color.RGBA) {
	dark := true
	theme := strings.ToLower(os.Getenv("GTK_THEME"))
	if strings.Contains(theme, "light") {
		dark = false
	} else if strings.Contains(theme, "dark") {
		dark = true
	}
	return dark, color.RGBA{R: 0x47, G: 0x74, B: 0xff, A: 0xff}
}

func paletteForAppearance(dark bool, accent color.RGBA) desktopPalette {
	if accent.A == 0 {
		accent = color.RGBA{R: 0x47, G: 0x74, B: 0xff, A: 0xff}
	}
	palette := desktopPalette{
		dark:          dark,
		background:    color.RGBA{R: 0x0b, G: 0x12, B: 0x20, A: 0xff},
		surface:       color.RGBA{R: 0x11, G: 0x1a, B: 0x2c, A: 0xff},
		surfaceStrong: color.RGBA{R: 0x18, G: 0x24, B: 0x3a, A: 0xff},
		line:          color.RGBA{R: 0x2b, G: 0x3a, B: 0x55, A: 0xff},
		text:          color.RGBA{R: 0xf7, G: 0xf9, B: 0xff, A: 0xff},
		muted:         color.RGBA{R: 0xb6, G: 0xb8, B: 0xbc, A: 0xff},
		controlMuted:  color.RGBA{R: 0x6a, G: 0x72, B: 0x8c, A: 0xff},
		button:        color.RGBA{R: 0x38, G: 0x3d, B: 0x4d, A: 0xff},
		buttonHot:     color.RGBA{R: 0x47, G: 0x4d, B: 0x60, A: 0xff},
		onButton:      color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
		accent:        accent,
		accentHot:     mixColor(accent, color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, 0.18),
		secondary:     color.RGBA{R: 0x9b, G: 0x5c, B: 0xff, A: 0xff},
		positive:      color.RGBA{R: 0x3d, G: 0xd6, B: 0x91, A: 0xff},
	}
	if !dark {
		palette.background = color.RGBA{R: 0xf5, G: 0xf7, B: 0xfc, A: 0xff}
		palette.surface = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
		palette.surfaceStrong = color.RGBA{R: 0xeb, G: 0xef, B: 0xf7, A: 0xff}
		palette.line = color.RGBA{R: 0xd4, G: 0xdc, B: 0xea, A: 0xff}
		palette.text = color.RGBA{R: 0x11, G: 0x18, B: 0x27, A: 0xff}
		palette.muted = color.RGBA{R: 0x61, G: 0x6d, B: 0x80, A: 0xff}
		palette.controlMuted = color.RGBA{R: 0x6b, G: 0x75, B: 0x88, A: 0xff}
		palette.button = color.RGBA{R: 0xe2, G: 0xe6, B: 0xef, A: 0xff}
		palette.buttonHot = color.RGBA{R: 0xd4, G: 0xda, B: 0xe7, A: 0xff}
		palette.onButton = color.RGBA{R: 0x12, G: 0x18, B: 0x26, A: 0xff}
		palette.accentHot = mixColor(accent, color.RGBA{A: 0xff}, 0.10)
	}
	palette.onAccent = readableForeground(accent)
	return palette
}

func readableForeground(background color.RGBA) color.RGBA {
	luminance := 0.2126*float64(background.R) + 0.7152*float64(background.G) + 0.0722*float64(background.B)
	if luminance > 150 {
		return color.RGBA{R: 0x0a, G: 0x0d, B: 0x14, A: 0xff}
	}
	return color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
}

func mixColor(left, right color.RGBA, amount float64) color.RGBA {
	if amount < 0 {
		amount = 0
	} else if amount > 1 {
		amount = 1
	}
	mix := func(a, b uint8) uint8 {
		return uint8(math.Round(float64(a)*(1-amount) + float64(b)*amount))
	}
	return color.RGBA{R: mix(left.R, right.R), G: mix(left.G, right.G), B: mix(left.B, right.B), A: 0xff}
}
