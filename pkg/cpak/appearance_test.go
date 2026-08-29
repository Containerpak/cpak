/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import "testing"

func TestApplyHostGTKThemeKeepsPackageChoice(t *testing.T) {
	environment := applyHostGTKTheme([]string{"GTK_THEME=HighContrast"}, "Adwaita:dark")
	if got := environmentValue(environment, "GTK_THEME"); got != "HighContrast" {
		t.Fatalf("package GTK theme changed to %q", got)
	}
}

func TestApplyHostGTKThemeAddsDetectedPreference(t *testing.T) {
	environment := applyHostGTKTheme([]string{"LANG=C"}, "Adwaita:dark")
	if got := environmentValue(environment, "GTK_THEME"); got != "Adwaita:dark" {
		t.Fatalf("host GTK theme was not inherited: %q", got)
	}
}

func TestGTKThemeForColorScheme(t *testing.T) {
	if got := gtkThemeForColorScheme(1); got != "Adwaita:dark" {
		t.Fatalf("dark preference mapped to %q", got)
	}
	if got := gtkThemeForColorScheme(2); got != "Adwaita" {
		t.Fatalf("light preference mapped to %q", got)
	}
	if got := gtkThemeForColorScheme(0); got != "" {
		t.Fatalf("unset preference mapped to %q", got)
	}
}
