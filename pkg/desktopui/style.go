/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"image"
	"image/color"

	"golang.org/x/image/font"
)

type DialogStyle struct {
	Background   color.RGBA
	Surface      color.RGBA
	SurfaceHover color.RGBA
	Line         color.RGBA
	Text         color.RGBA
	Muted        color.RGBA
	ControlMuted color.RGBA
	Button       color.RGBA
	ButtonHover  color.RGBA
	OnButton     color.RGBA
	Accent       color.RGBA
	AccentHover  color.RGBA
	OnAccent     color.RGBA
	Positive     color.RGBA
	Negative     color.RGBA
	Dark         bool
}

func CurrentDialogStyle() DialogStyle {
	return dialogStyleFromPalette(currentDesktopPalette())
}

func dialogStyleFromPalette(palette desktopPalette) DialogStyle {
	return DialogStyle{
		Background:   palette.background,
		Surface:      palette.surface,
		SurfaceHover: palette.surfaceStrong,
		Line:         palette.line,
		Text:         palette.text,
		Muted:        palette.muted,
		ControlMuted: palette.controlMuted,
		Button:       palette.button,
		ButtonHover:  palette.buttonHot,
		OnButton:     palette.onButton,
		Accent:       palette.accent,
		AccentHover:  palette.accentHot,
		OnAccent:     palette.onAccent,
		Positive:     palette.positive,
		Negative:     updateBad,
		Dark:         palette.dark,
	}
}

func PaintDialogBackdrop(target *image.RGBA, style DialogStyle) {
	drawGrantBackdrop(target, style.desktopPalette())
}

func PaintDialogBrand(target *image.RGBA, style DialogStyle) {
	drawGrantBrand(target, style.desktopPalette(), brandIconPNG)
}

func DialogFont(size int, semibold bool) font.Face {
	return updateFont(size, semibold)
}

func (style DialogStyle) ActionColors(recommended, hovered, enabled bool) (color.RGBA, color.RGBA) {
	if !enabled {
		return style.SurfaceHover, style.Muted
	}
	if recommended {
		if hovered {
			return style.AccentHover, style.OnAccent
		}
		return style.Accent, style.OnAccent
	}
	if hovered {
		return style.ButtonHover, style.OnButton
	}
	return style.Button, style.OnButton
}

func (style DialogStyle) desktopPalette() desktopPalette {
	return desktopPalette{
		dark:          style.Dark,
		background:    style.Background,
		surface:       style.Surface,
		surfaceStrong: style.SurfaceHover,
		line:          style.Line,
		text:          style.Text,
		muted:         style.Muted,
		controlMuted:  style.ControlMuted,
		button:        style.Button,
		buttonHot:     style.ButtonHover,
		onButton:      style.OnButton,
		accent:        style.Accent,
		accentHot:     style.AccentHover,
		onAccent:      style.OnAccent,
		positive:      style.Positive,
	}
}
