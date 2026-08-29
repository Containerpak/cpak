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
)

const (
	portalAppearanceNamespace = "org.freedesktop.appearance"
	portalSettingsReadOne     = "org.freedesktop.portal.Settings.ReadOne"
)

var (
	hostGTKThemeOnce sync.Once
	hostGTKTheme     string
)

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
