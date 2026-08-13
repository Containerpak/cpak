/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package desktopui

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/mobile/event/key"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/mouse"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
)

const (
	updateReady = iota
	updateInstalling
	updateDone
	updateFailed
)

var (
	updateBackground = color.RGBA{0x15, 0x1f, 0x33, 0xff}
	updateLine       = color.RGBA{0x2a, 0x3b, 0x5c, 0xff}
	updatePrimary    = color.RGBA{0x3e, 0x7b, 0xff, 0xff}
	updatePrimaryHot = color.RGBA{0x60, 0x94, 0xff, 0xff}
	updateText       = color.RGBA{0xf5, 0xf7, 0xff, 0xff}
	updateMuted      = color.RGBA{0xa8, 0xb4, 0xca, 0xff}
	updateGood       = color.RGBA{0x3d, 0xd6, 0x91, 0xff}
	updateBad        = color.RGBA{0xff, 0x6b, 0x7b, 0xff}
	updateFaces      sync.Map
)

type updateState struct {
	sync.Mutex
	phase        int
	status       string
	hovered      bool
	closeHovered bool
	frame        int
}

type updateEvent struct{}

func updateBuiltin(request UpdateRequest, action func(func(string)) error) error {
	var windowErr error
	driver.Main(func(display screen.Screen) {
		const width, height = 560, 600
		window, err := display.NewWindow(&screen.NewWindowOptions{Width: width, Height: height, Title: "cpak update"})
		if err != nil {
			windowErr = err
			return
		}
		defer window.Release()

		frame := newDesktopFrame("cpak update")
		if frame != nil {
			defer frame.Close()
		}
		state := &updateState{status: "Ready to update"}
		if request.Managed {
			state.status = "Managed by your package manager"
		}
		icon := renderUpdateIcon(request.IconPNG, 104)
		var dimensions size.Event
		button := updateButton(width, height)
		for {
			event := window.NextEvent()
			switch event := event.(type) {
			case lifecycle.Event:
				if event.To == lifecycle.StageDead {
					return
				}
			case size.Event:
				dimensions = event
				button = updateButton(event.WidthPx, event.HeightPx)
				window.Send(paint.Event{})
			case paint.Event, updateEvent:
				renderUpdate(display, window, dimensions, button, request, icon, state)
			case mouse.Event:
				point := image.Pt(int(event.X), int(event.Y))
				hovered := point.In(button)
				closeHovered := point.In(updateClose(dimensions.WidthPx))
				state.Lock()
				changed := state.hovered != hovered || state.closeHovered != closeHovered
				state.hovered = hovered
				state.closeHovered = closeHovered
				phase := state.phase
				state.Unlock()
				if changed {
					window.Send(paint.Event{})
				}
				if event.Button != mouse.ButtonLeft || event.Direction != mouse.DirPress {
					continue
				}
				if closeHovered || hovered && (request.Managed || phase == updateDone) {
					return
				}
				if hovered && phase != updateInstalling {
					startBuiltinUpdate(window, state, action)
				} else if point.Y < 54 && frame != nil {
					frame.StartMove()
				}
			case key.Event:
				if event.Code == key.CodeEscape && event.Direction == key.DirPress {
					return
				}
			}
		}
	})
	return windowErr
}

func startBuiltinUpdate(window screen.Window, state *updateState, action func(func(string)) error) {
	state.Lock()
	state.phase = updateInstalling
	state.status = "Downloading cpak"
	state.frame = 0
	state.Unlock()
	window.Send(updateEvent{})

	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(70 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				state.Lock()
				state.frame++
				state.Unlock()
				window.Send(updateEvent{})
			case <-stop:
				return
			}
		}
	}()
	go func() {
		err := action(func(message string) {
			state.Lock()
			state.status = message
			state.Unlock()
			window.Send(updateEvent{})
		})
		close(stop)
		state.Lock()
		if err != nil {
			state.phase = updateFailed
			state.status = err.Error()
		} else {
			state.phase = updateDone
			state.status = "cpak is up to date"
		}
		state.Unlock()
		window.Send(updateEvent{})
	}()
}

func renderUpdate(display screen.Screen, window screen.Window, dimensions size.Event, button image.Rectangle, request UpdateRequest, icon image.Image, state *updateState) {
	width, height := dimensions.WidthPx, dimensions.HeightPx
	if width <= 0 || height <= 0 {
		return
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(updateBackground), image.Point{}, draw.Src)
	drawUpdateOutline(canvas, canvas.Bounds(), updateLine)
	draw.Draw(canvas, image.Rect(width/2-52, 56, width/2+52, 160), icon, image.Point{}, draw.Over)
	drawUpdateCentered(canvas, "cpak "+request.Version, width/2, 204, 24, true, updateText)
	drawUpdateCentered(canvas, "A new version is available", width/2, 234, 15, false, updateMuted)

	notes := summarizeNotes(request.Notes, 360)
	if request.Managed {
		notes = "Your package manager owns this installation. Ask its maintainer to publish the update."
	}
	if notes == "" {
		notes = "Installed version: " + request.CurrentVersion
	}
	drawUpdateWrapped(canvas, notes, image.Rect(72, 274, width-72, 410), 13, updateMuted)

	state.Lock()
	phase, status, hovered, closeHovered, frame := state.phase, state.status, state.hovered, state.closeHovered, state.frame
	state.Unlock()
	drawUpdateClose(canvas, updateClose(width), closeHovered)
	statusColor := updateMuted
	if phase == updateDone {
		statusColor = updateGood
	} else if phase == updateFailed {
		statusColor = updateBad
	}
	drawUpdateCenteredFitted(canvas, status, width/2, button.Min.Y-36, width-72, 14, false, statusColor)
	if phase == updateInstalling {
		drawUpdateProgress(canvas, image.Rect(button.Min.X, button.Min.Y-20, button.Max.X, button.Min.Y-14), frame)
	}
	buttonColor := updatePrimary
	if hovered && phase != updateInstalling {
		buttonColor = updatePrimaryHot
	}
	if phase == updateInstalling {
		buttonColor = color.RGBA{0x2a, 0x47, 0x7f, 0xff}
	}
	drawUpdateRounded(canvas, button, 14, buttonColor)
	label := "Update"
	if request.Managed {
		label = "Close"
	} else if phase == updateInstalling {
		label = "Updating..."
	} else if phase == updateDone {
		label = "Close"
	} else if phase == updateFailed {
		label = "Try again"
	}
	drawUpdateCentered(canvas, label, width/2, button.Min.Y+33, 16, true, updateText)

	buffer, err := display.NewBuffer(image.Pt(width, height))
	if err != nil {
		return
	}
	defer buffer.Release()
	draw.Draw(buffer.RGBA(), buffer.Bounds(), canvas, image.Point{}, draw.Src)
	window.Upload(image.Point{}, buffer, buffer.Bounds())
	window.Publish()
}

func renderUpdateIcon(encoded []byte, size int) image.Image {
	target := image.NewRGBA(image.Rect(0, 0, size, size))
	icon, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		drawUpdateRounded(target, target.Bounds(), 18, updatePrimary)
		drawUpdateCentered(target, "c", size/2, size/2+20, 52, true, updateText)
		return target
	}
	bounds := icon.Bounds()
	left := (size - bounds.Dx()) / 2
	top := (size - bounds.Dy()) / 2
	destination := image.Rect(left, top, left+bounds.Dx(), top+bounds.Dy())
	draw.Draw(target, destination, icon, bounds.Min, draw.Over)
	return target
}

func updateButton(width, height int) image.Rectangle {
	const buttonWidth = 268
	left := (width - buttonWidth) / 2
	return image.Rect(left, height-90, left+buttonWidth, height-38)
}

func updateClose(width int) image.Rectangle {
	return image.Rect(width-48, 16, width-16, 48)
}

func drawUpdateClose(target *image.RGBA, bounds image.Rectangle, hovered bool) {
	fill := color.RGBA{0x1d, 0x2a, 0x43, 0xff}
	if hovered {
		fill = updateLine
	}
	drawUpdateRounded(target, bounds, bounds.Dx()/2, fill)
	for offset := -1; offset <= 1; offset++ {
		for index := 0; index < 10; index++ {
			target.Set(bounds.Min.X+11+index, bounds.Min.Y+11+index+offset, updateText)
			target.Set(bounds.Max.X-12-index, bounds.Min.Y+11+index+offset, updateText)
		}
	}
}

func drawUpdateProgress(target *image.RGBA, bounds image.Rectangle, frame int) {
	drawUpdateRounded(target, bounds, 3, updateLine)
	span := bounds.Dx() / 3
	left := bounds.Min.X + (frame*7)%(bounds.Dx()+span) - span
	drawUpdateRounded(target, image.Rect(left, bounds.Min.Y, left+span, bounds.Max.Y).Intersect(bounds), 3, updatePrimary)
}

func drawUpdateRounded(target *image.RGBA, bounds image.Rectangle, radius int, fill color.Color) {
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			dx := max(bounds.Min.X+radius-x, x-(bounds.Max.X-radius-1), 0)
			dy := max(bounds.Min.Y+radius-y, y-(bounds.Max.Y-radius-1), 0)
			if dx*dx+dy*dy <= radius*radius {
				target.Set(x, y, fill)
			}
		}
	}
}

func drawUpdateOutline(target *image.RGBA, bounds image.Rectangle, fill color.Color) {
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Min.Y+1), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Max.Y-1, bounds.Max.X, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Min.X+1, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Max.X-1, bounds.Min.Y, bounds.Max.X, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
}

func drawUpdateCentered(target draw.Image, value string, centerX, baseline, size int, bold bool, fill color.Color) {
	face := updateFont(size, bold)
	width := font.MeasureString(face, value).Round()
	drawer := font.Drawer{Dst: target, Src: image.NewUniform(fill), Face: face, Dot: fixed.P(centerX-width/2, baseline)}
	drawer.DrawString(value)
}

func drawUpdateCenteredFitted(target draw.Image, value string, centerX, baseline, maxWidth, size int, bold bool, fill color.Color) {
	face := updateFont(size, bold)
	value = fitUpdateText(strings.Join(strings.Fields(value), " "), maxWidth, face)
	drawUpdateCentered(target, value, centerX, baseline, size, bold, fill)
}

func fitUpdateText(value string, maxWidth int, face font.Face) string {
	if font.MeasureString(face, value).Round() <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 3 && font.MeasureString(face, string(runes)+"...").Round() > maxWidth {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func drawUpdateWrapped(target draw.Image, value string, bounds image.Rectangle, size int, fill color.Color) {
	face := updateFont(size, false)
	baseline := bounds.Min.Y + size
	for _, paragraph := range strings.Split(value, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			candidate := strings.TrimSpace(line + " " + word)
			if line != "" && font.MeasureString(face, candidate).Round() > bounds.Dx() {
				drawUpdateCenteredFitted(target, line, bounds.Min.X+bounds.Dx()/2, baseline, bounds.Dx(), size, false, fill)
				baseline += size + 7
				line = word
			} else {
				line = candidate
			}
		}
		if line != "" {
			drawUpdateCenteredFitted(target, line, bounds.Min.X+bounds.Dx()/2, baseline, bounds.Dx(), size, false, fill)
			baseline += size + 10
		}
		if baseline > bounds.Max.Y {
			return
		}
	}
}

func updateFont(size int, bold bool) font.Face {
	key := struct {
		size int
		bold bool
	}{size: size, bold: bold}
	if cached, ok := updateFaces.Load(key); ok {
		return cached.(font.Face)
	}
	data := goregular.TTF
	if bold {
		data = gobold.TTF
	}
	parsed, _ := opentype.Parse(data)
	face, _ := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 96, Hinting: font.HintingFull})
	updateFaces.Store(key, face)
	return face
}
