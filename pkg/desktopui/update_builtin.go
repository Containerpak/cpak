/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	xdraw "golang.org/x/image/draw"
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
	interFontsOnce   sync.Once
	interRegular     []byte
	interSemibold    []byte
)

type updateState struct {
	sync.Mutex
	phase        int
	status       string
	hovered      bool
	closeHovered bool
	frame        int
	palette      desktopPalette
}

type updateEvent struct{}

func updateBuiltin(request UpdateRequest, action func(func(string)) error) error {
	var windowErr error
	driver.Main(func(display screen.Screen) {
		const width, height = 560, 600
		decoration := captureDesktopWindows()
		window, err := display.NewWindow(&screen.NewWindowOptions{Width: width, Height: height, Title: "cpak update"})
		if err != nil {
			decoration.Close()
			windowErr = err
			return
		}
		decoration.Apply("cpak update", request.IconPNG)
		defer window.Release()

		frame := newDesktopFrame("cpak update")
		if frame != nil {
			defer frame.Close()
		}
		state := &updateState{status: "Ready to update", palette: currentDesktopPalette()}
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
	palette := state.palette
	drawGrantBackdrop(canvas, palette)
	drawUpdateOutline(canvas, canvas.Bounds(), palette.line)
	drawGrantBrand(canvas, palette, brandIconPNG)
	draw.Draw(canvas, image.Rect(width/2-52, 64, width/2+52, 168), icon, image.Point{}, draw.Over)
	drawUpdateCentered(canvas, "cpak "+request.Version, width/2, 212, 24, true, palette.text)
	drawUpdateCentered(canvas, "A new version is available", width/2, 242, 15, false, palette.muted)

	notes := summarizeNotes(request.Notes, 360)
	if request.Managed {
		notes = "Your package manager owns this installation. Ask its maintainer to publish the update."
	}
	if notes == "" {
		notes = "Installed version: " + request.CurrentVersion
	}
	drawUpdateWrapped(canvas, notes, image.Rect(72, 274, width-72, 410), 13, palette.muted)

	state.Lock()
	phase, status, hovered, closeHovered, frame := state.phase, state.status, state.hovered, state.closeHovered, state.frame
	state.Unlock()
	drawGrantClose(canvas, updateClose(width), closeHovered, palette)
	statusColor := palette.muted
	if phase == updateDone {
		statusColor = palette.positive
	} else if phase == updateFailed {
		statusColor = updateBad
	}
	drawUpdateCenteredFitted(canvas, status, width/2, button.Min.Y-36, width-72, 14, false, statusColor)
	if phase == updateInstalling {
		drawUpdateProgressColor(canvas, image.Rect(button.Min.X, button.Min.Y-20, button.Max.X, button.Min.Y-14), frame, palette.line, palette.accent)
	}
	style := dialogStyleFromPalette(palette)
	recommended := !request.Managed && phase != updateDone
	buttonColor, buttonText := style.ActionColors(recommended, hovered, phase != updateInstalling)
	drawUpdateRounded(canvas, button, button.Dy()/2, buttonColor)
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
	drawUpdateCentered(canvas, label, width/2, button.Min.Y+34, 18, true, buttonText)

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
	width, height := bounds.Dx(), bounds.Dy()
	if width > size || height > size {
		scale := min(float64(size)/float64(width), float64(size)/float64(height))
		width = max(1, int(float64(width)*scale))
		height = max(1, int(float64(height)*scale))
	}
	left := (size - width) / 2
	top := (size - height) / 2
	destination := image.Rect(left, top, left+width, top+height)
	xdraw.CatmullRom.Scale(target, destination, icon, bounds, draw.Over, nil)
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

func drawUpdateProgress(target *image.RGBA, bounds image.Rectangle, frame int) {
	drawUpdateProgressColor(target, bounds, frame, updateLine, updatePrimary)
}

func drawUpdateProgressColor(target *image.RGBA, bounds image.Rectangle, frame int, track, active color.RGBA) {
	drawUpdateRounded(target, bounds, 3, track)
	span := bounds.Dx() / 3
	left := bounds.Min.X + (frame*7)%(bounds.Dx()+span) - span
	drawUpdateRounded(target, image.Rect(left, bounds.Min.Y, left+span, bounds.Max.Y).Intersect(bounds), 3, active)
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

func drawUpdateText(target draw.Image, value string, x, baseline, size int, bold bool, fill color.Color) {
	face := updateFont(size, bold)
	drawer := font.Drawer{Dst: target, Src: image.NewUniform(fill), Face: face, Dot: fixed.P(x, baseline)}
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
	data := desktopFontData(bold)
	parsed, err := opentype.Parse(data)
	if err != nil {
		data = goregular.TTF
		if bold {
			data = gobold.TTF
		}
		parsed, _ = opentype.Parse(data)
	}
	face, _ := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	updateFaces.Store(key, face)
	return face
}

func desktopFontData(semibold bool) []byte {
	interFontsOnce.Do(func() {
		home, _ := os.UserHomeDir()
		interRegular = readDesktopFont([]string{
			filepath.Join(home, ".local", "share", "fonts", "inter", "Inter-Regular.ttf"),
			"/usr/share/fonts/TTF/Inter-Regular.ttf",
			"/usr/share/fonts/truetype/inter/Inter-Regular.ttf",
		})
		interSemibold = readDesktopFont([]string{
			filepath.Join(home, ".local", "share", "fonts", "inter", "Inter-SemiBold.ttf"),
			"/usr/share/fonts/TTF/Inter-SemiBold.ttf",
			"/usr/share/fonts/truetype/inter/Inter-SemiBold.ttf",
		})
	})
	if semibold && len(interSemibold) > 0 {
		return interSemibold
	}
	if !semibold && len(interRegular) > 0 {
		return interRegular
	}
	if semibold {
		return gobold.TTF
	}
	return goregular.TTF
}

func readDesktopFont(paths []string) []byte {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
	}
	return nil
}
