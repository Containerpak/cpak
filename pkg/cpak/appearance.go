/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/mirkobrombin/cpak/pkg/desktopbus"
)

const (
	portalAppearanceNamespace = "org.freedesktop.appearance"
	portalSettingsReadOne     = "org.freedesktop.portal.Settings.ReadOne"
)

var (
	hostGTKThemeOnce sync.Once
	hostGTKTheme     string

	hostFontSettingsOnce sync.Once
	hostFontSettings     [][2]string
)

func detectedHostFontSettings() [][2]string {
	hostFontSettingsOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		hostFontSettings = readHostFontSettings(ctx)
	})
	return hostFontSettings
}

func readHostFontSettings(ctx context.Context) [][2]string {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil
	}
	defer connection.Close()
	object := connection.Object("org.freedesktop.portal.Desktop", dbus.ObjectPath("/org/freedesktop/portal/desktop"))
	var settings [][2]string
	for _, key := range []string{"font-antialiasing", "font-hinting", "font-rgba-order"} {
		var value dbus.Variant
		if object.CallWithContext(ctx, portalSettingsReadOne, 0, "org.gnome.desktop.interface", key).Store(&value) != nil {
			continue
		}
		for {
			nested, ok := value.Value().(dbus.Variant)
			if !ok {
				break
			}
			value = nested
		}
		if text, ok := value.Value().(string); ok && desktopbus.ValidFontSetting(key, text) {
			settings = append(settings, [2]string{key, text})
		}
	}
	return settings
}

func inheritHostAppearance(environment []string) []string {
	return applyHostGTKTheme(environment, detectedHostGTKTheme())
}

func applyHostGTKTheme(environment []string, theme string) []string {
	if theme == "" || environmentValue(environment, "GTK_THEME") != "" {
		return environment
	}
	return setEnvironmentValue(environment, "GTK_THEME", theme)
}

func detectedHostGTKTheme() string {
	hostGTKThemeOnce.Do(func() {
		if theme := os.Getenv("GTK_THEME"); theme != "" {
			hostGTKTheme = theme
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
		defer cancel()
		hostGTKTheme = readHostGTKTheme(ctx)
	})
	return hostGTKTheme
}

func readHostGTKTheme(ctx context.Context) string {
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return ""
	}
	defer connection.Close()
	object := connection.Object("org.freedesktop.portal.Desktop", dbus.ObjectPath("/org/freedesktop/portal/desktop"))
	var value dbus.Variant
	if err = object.CallWithContext(ctx, portalSettingsReadOne, 0, portalAppearanceNamespace, "color-scheme").Store(&value); err != nil {
		return ""
	}
	for {
		nested, ok := value.Value().(dbus.Variant)
		if !ok {
			break
		}
		value = nested
	}
	scheme, ok := value.Value().(uint32)
	if !ok {
		return ""
	}
	return gtkThemeForColorScheme(scheme)
}

func gtkThemeForColorScheme(scheme uint32) string {
	switch scheme {
	case 1:
		return "Adwaita:dark"
	case 2:
		return "Adwaita"
	default:
		return ""
	}
}
