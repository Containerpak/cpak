/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"
	"sync"

	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	"golang.org/x/mobile/event/key"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/mouse"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
)

type fileGrantPromptState struct {
	containingFolder bool
	persistent       bool
	hovered          int
	palette          desktopPalette
}

type fileGrantPromptEvent struct{}

var grantBackdropCache sync.Map

func confirmFileGrantBuiltin(ctx context.Context, result FilePickerResult, request FilePickerRequest) (FilePickerResult, error) {
	var windowErr error
	accepted := false
	driver.Main(func(display screen.Screen) {
		const width, height = 520, 391
		decoration := captureDesktopWindows()
		window, err := display.NewWindow(&screen.NewWindowOptions{Width: width, Height: height, Title: "cpak file access"})
		if err != nil {
			decoration.Close()
			windowErr = err
			return
		}
		decoration.Apply("cpak file access", brandIconPNG)
		defer window.Release()
		frame := newDesktopFrame("cpak file access")
		if frame != nil {
			frame.SetDialog(request.ParentWindow)
			defer frame.Close()
		}
		state := &fileGrantPromptState{
			containingFolder: result.ContainingFolder,
			persistent:       result.Persistent,
			hovered:          -1,
			palette:          currentDesktopPalette(),
		}
		var dimensions size.Event
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				window.Send(fileGrantPromptEvent{})
			case <-stop:
			}
		}()
		for {
			event := window.NextEvent()
			switch event := event.(type) {
			case lifecycle.Event:
				if event.To == lifecycle.StageDead {
					return
				}
			case size.Event:
				dimensions = event
				window.Send(paint.Event{})
			case paint.Event:
				renderFileGrantPrompt(display, window, dimensions, result.Path, request, state)
			case fileGrantPromptEvent:
				if ctx.Err() != nil {
					windowErr = ctx.Err()
					return
				}
			case mouse.Event:
				point := image.Pt(int(event.X), int(event.Y))
				state.hovered = fileGrantControlAt(point, dimensions.WidthPx, dimensions.HeightPx, request)
				window.Send(paint.Event{})
				if event.Button != mouse.ButtonLeft || event.Direction != mouse.DirPress {
					continue
				}
				switch state.hovered {
				case 0:
					state.containingFolder = !state.containingFolder
				case 1:
					state.persistent = !state.persistent
				case 2:
					return
				case 3:
					result.ContainingFolder = state.containingFolder
					result.Persistent = state.persistent
					accepted = true
					return
				}
				if point.Y < 54 && frame != nil {
					frame.StartMove()
				}
			case key.Event:
				if event.Code == key.CodeEscape && event.Direction == key.DirPress {
					return
				}
			}
		}
	})
	return fileGrantPromptResult(result, accepted, windowErr)
}

func fileGrantPromptResult(result FilePickerResult, accepted bool, err error) (FilePickerResult, error) {
	if err != nil {
		return FilePickerResult{}, err
	}
	if !accepted {
		return FilePickerResult{}, ErrCancelled
	}
	return result, nil
}

func renderFileGrantPrompt(display screen.Screen, window screen.Window, dimensions size.Event, selected string, request FilePickerRequest, state *fileGrantPromptState) {
	width, height := dimensions.WidthPx, dimensions.HeightPx
	if width <= 0 || height <= 0 {
		return
	}
	canvas := fileGrantPromptCanvas(width, height, selected, request, state)
	buffer, err := display.NewBuffer(image.Pt(width, height))
	if err != nil {
		return
	}
	defer buffer.Release()
	draw.Draw(buffer.RGBA(), buffer.Bounds(), canvas, image.Point{}, draw.Src)
	window.Upload(image.Point{}, buffer, buffer.Bounds())
	window.Publish()
}

func fileGrantPromptCanvas(width, height int, selected string, request FilePickerRequest, state *fileGrantPromptState) *image.RGBA {
	palette := state.palette
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	drawGrantBackdrop(canvas, palette)
	drawUpdateOutline(canvas, canvas.Bounds(), palette.line)
	drawGrantBrand(canvas, palette, brandIconPNG)
	drawUpdateCentered(canvas, "Allow access?", width/2, 100, 27, true, palette.text)
	drawGrantApplication(canvas, request.Application, width/2, 137, width-60, palette)
	drawUpdateCenteredFitted(canvas, selected, width/2, 160, width-80, 14, false, palette.muted)

	y := fileGrantCheckboxStart(request)
	if request.OfferContainingFolder {
		drawGrantCheckbox(canvas, fileGrantCheckbox(width, y), "Give access to the parent folder?", state.containingFolder, state.hovered == 0, palette)
		y += 44
	}
	if request.OfferPersistent {
		drawGrantCheckbox(canvas, fileGrantCheckbox(width, y), "Remember for this resource?", state.persistent, state.hovered == 1, palette)
	}
	drawGrantAction(canvas, fileGrantAction(width, height, 0), "Deny", state.hovered == 2, palette)
	drawGrantAction(canvas, fileGrantAction(width, height, 1), "Allow", state.hovered == 3, palette)
	return canvas
}

func drawGrantApplication(target *image.RGBA, application string, centerX, baseline, maxWidth int, palette desktopPalette) {
	application = strings.TrimSpace(application)
	if application == "" {
		application = "This application"
	}
	suffix := " wants access to:"
	regular := updateFont(19, false)
	semibold := updateFont(19, true)
	suffixWidth := font.MeasureString(regular, suffix).Round()
	application = fitUpdateText(application, maxWidth-suffixWidth, semibold)
	width := font.MeasureString(semibold, application).Round() + suffixWidth
	drawer := font.Drawer{Dst: target, Src: image.NewUniform(palette.muted), Face: semibold, Dot: fixed.P(centerX-width/2, baseline)}
	drawer.DrawString(application)
	drawer.Face = regular
	drawer.DrawString(suffix)
}

func drawGrantCheckbox(target *image.RGBA, bounds image.Rectangle, label string, checked, hovered bool, palette desktopPalette) {
	outline := palette.controlMuted
	if hovered {
		outline = mixColor(palette.controlMuted, palette.text, 0.24)
	}
	if checked {
		drawUpdateRounded(target, bounds, 7, palette.accent)
		drawGrantCheck(target, bounds, palette.onAccent)
	} else {
		drawGrantRoundedOutline(target, bounds, 7, 3, outline)
	}
	drawUpdateText(target, label, bounds.Max.X+12, bounds.Min.Y+18, 16, false, palette.controlMuted)
}

func drawGrantCheck(target *image.RGBA, bounds image.Rectangle, fill color.RGBA) {
	for offset := -1; offset <= 1; offset++ {
		for index := 0; index < 6; index++ {
			target.Set(bounds.Min.X+5+index, bounds.Min.Y+12+index/2+offset, fill)
		}
		for index := 0; index < 10; index++ {
			target.Set(bounds.Min.X+10+index, bounds.Min.Y+15-index/2+offset, fill)
		}
	}
}

func drawGrantRoundedOutline(target *image.RGBA, bounds image.Rectangle, radius, stroke int, fill color.RGBA) {
	inner := bounds.Inset(stroke)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if roundedPoint(bounds, radius, x, y) && !roundedPoint(inner, max(0, radius-stroke), x, y) {
				target.Set(x, y, fill)
			}
		}
	}
}

func roundedPoint(bounds image.Rectangle, radius, x, y int) bool {
	dx := max(bounds.Min.X+radius-x, x-(bounds.Max.X-radius-1), 0)
	dy := max(bounds.Min.Y+radius-y, y-(bounds.Max.Y-radius-1), 0)
	return dx*dx+dy*dy <= radius*radius
}

func drawGrantAction(target *image.RGBA, bounds image.Rectangle, label string, hovered bool, palette desktopPalette) {
	style := dialogStyleFromPalette(palette)
	fill, text := style.ActionColors(false, hovered, true)
	drawUpdateRounded(target, bounds, bounds.Dy()/2, fill)
	drawUpdateCentered(target, label, bounds.Min.X+bounds.Dx()/2, bounds.Min.Y+31, 18, true, text)
}

func drawGrantBackdrop(target *image.RGBA, palette desktopPalette) {
	bounds := target.Bounds()
	key := struct {
		Size       image.Point
		Background color.RGBA
		Accent     color.RGBA
		Secondary  color.RGBA
		Dark       bool
	}{bounds.Size(), palette.background, palette.accent, palette.secondary, palette.dark}
	if cached, ok := grantBackdropCache.Load(key); ok {
		draw.Draw(target, bounds, cached.(*image.RGBA), image.Point{}, draw.Src)
		return
	}
	backdrop := image.NewRGBA(bounds)
	strength := 0.12
	if !palette.dark {
		strength = 0.06
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			accentWeight := radialWeight(x, y, -30, 10, 320) * strength
			lowerWeight := radialWeight(x, y, bounds.Max.X+70, bounds.Max.Y+80, 380) * strength * 0.52
			pixel := mixColor(palette.background, palette.accent, accentWeight)
			pixel = mixColor(pixel, palette.accent, lowerWeight)
			backdrop.SetRGBA(x, y, pixel)
		}
	}
	grantBackdropCache.Store(key, backdrop)
	draw.Draw(target, bounds, backdrop, image.Point{}, draw.Src)
}

func radialWeight(x, y, centerX, centerY, radius int) float64 {
	distance := math.Hypot(float64(x-centerX), float64(y-centerY))
	if distance >= float64(radius) {
		return 0
	}
	ratio := 1 - distance/float64(radius)
	return ratio * ratio
}

func drawGrantBrand(target *image.RGBA, palette desktopPalette, iconPNG []byte) {
	textX := 18
	if len(iconPNG) > 0 {
		icon := renderUpdateIcon(iconPNG, 31)
		draw.Draw(target, image.Rect(18, 17, 49, 48), icon, image.Point{}, draw.Over)
		textX = 52
	}
	drawUpdateText(target, "cpak", textX, 38, 14, true, palette.text)
}

func drawGrantClose(target *image.RGBA, bounds image.Rectangle, hovered bool, palette desktopPalette) {
	fill := palette.surfaceStrong
	if hovered {
		fill = mixColor(palette.surfaceStrong, palette.accent, 0.18)
	}
	drawUpdateRounded(target, bounds, bounds.Dx()/2, fill)
	for offset := -1; offset <= 1; offset++ {
		for index := 0; index < 10; index++ {
			target.Set(bounds.Min.X+11+index, bounds.Min.Y+11+index+offset, palette.text)
			target.Set(bounds.Max.X-12-index, bounds.Min.Y+11+index+offset, palette.text)
		}
	}
}

func fileGrantControlAt(point image.Point, width, height int, request FilePickerRequest) int {
	y := fileGrantCheckboxStart(request)
	if request.OfferContainingFolder {
		if point.In(fileGrantCheckboxRow(width, y)) {
			return 0
		}
		y += 44
	}
	if request.OfferPersistent {
		if point.In(fileGrantCheckboxRow(width, y)) {
			return 1
		}
	}
	for index := 0; index < 2; index++ {
		if point.In(fileGrantAction(width, height, index)) {
			return 2 + index
		}
	}
	return -1
}

func fileGrantCheckboxStart(request FilePickerRequest) int {
	if request.OfferContainingFolder && request.OfferPersistent {
		return 202
	}
	return 224
}

func fileGrantCheckbox(width, y int) image.Rectangle {
	left := width/2 - 142
	return image.Rect(left, y, left+24, y+24)
}

func fileGrantCheckboxRow(width, y int) image.Rectangle {
	checkbox := fileGrantCheckbox(width, y)
	return image.Rect(checkbox.Min.X, y-4, width-checkbox.Min.X, y+28)
}

func fileGrantAction(width, height, index int) image.Rectangle {
	const buttonWidth, gap = 141, 17
	left := (width-(buttonWidth*2+gap))/2 + index*(buttonWidth+gap)
	return image.Rect(left, height-90, left+buttonWidth, height-42)
}
