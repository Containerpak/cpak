/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
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
	phaseReady = iota
	phaseInstalling
	phaseDone
	phaseFailed
)

var (
	card       = color.RGBA{0x15, 0x1f, 0x33, 0xff}
	cardLine   = color.RGBA{0x2a, 0x3b, 0x5c, 0xff}
	primary    = color.RGBA{0x3e, 0x7b, 0xff, 0xff}
	primaryHot = color.RGBA{0x60, 0x94, 0xff, 0xff}
	textMain   = color.RGBA{0xf5, 0xf7, 0xff, 0xff}
	textMuted  = color.RGBA{0xa8, 0xb4, 0xca, 0xff}
	good       = color.RGBA{0x3d, 0xd6, 0x91, 0xff}
	bad        = color.RGBA{0xff, 0x6b, 0x7b, 0xff}
	faceCache  sync.Map
)

type guiState struct {
	sync.Mutex
	phase              int
	status             string
	hovered            bool
	closeHovered       bool
	permissionsHovered bool
	permissionsOpen    bool
	frame              int
}

type guiUpdate struct{}

func runGUI(capsule bootstrap.Capsule) {
	driver.Main(func(s screen.Screen) {
		const width, height = 552, 540
		windowTitle := fmt.Sprintf("cpak-installer-%d", os.Getpid())
		window, err := s.NewWindow(&screen.NewWindowOptions{
			Width:  width,
			Height: height,
			Title:  windowTitle,
		})
		if err != nil {
			fail(err)
		}
		defer window.Release()
		frame := newX11Frame(windowTitle)
		if frame != nil {
			defer frame.Close()
		}

		state := &guiState{status: "Ready to install"}
		icon := renderIcon(capsule.Metadata.IconSVG, capsule.Metadata.Name, 108)
		var dimensions size.Event
		button := buttonRect(width, height)
		for {
			event := window.NextEvent()
			switch event := event.(type) {
			case lifecycle.Event:
				if event.To == lifecycle.StageDead {
					return
				}
			case size.Event:
				dimensions = event
				button = buttonRect(event.WidthPx, event.HeightPx)
				window.Send(paint.Event{})
			case paint.Event, guiUpdate:
				renderGUI(s, window, dimensions, button, capsule.Metadata, icon, state)
			case mouse.Event:
				point := image.Pt(int(event.X), int(event.Y))
				hovered := point.In(button)
				closeHovered := point.In(closeRect(dimensions.WidthPx))
				permissionsHovered := len(capsule.Metadata.Permissions) > 0 && point.In(permissionsRect(dimensions.WidthPx))
				state.Lock()
				changed := state.hovered != hovered || state.closeHovered != closeHovered || state.permissionsHovered != permissionsHovered
				state.hovered = hovered
				state.closeHovered = closeHovered
				state.permissionsHovered = permissionsHovered
				phase := state.phase
				state.Unlock()
				if changed {
					window.Send(paint.Event{})
				}
				if event.Button == mouse.ButtonLeft && event.Direction == mouse.DirPress {
					if closeHovered {
						return
					}
					if permissionsHovered && frame != nil {
						state.Lock()
						state.permissionsOpen = !state.permissionsOpen
						permissionsOpen := state.permissionsOpen
						state.Unlock()
						height := 540
						if permissionsOpen {
							height = expandedWindowHeight(len(capsule.Metadata.Permissions))
						}
						frame.Resize(width, height)
					} else if hovered {
						if phase == phaseDone {
							return
						}
						if phase != phaseInstalling {
							startGUIInstall(window, capsule, state)
						}
					} else if point.Y < 54 && frame != nil {
						frame.StartMove()
					}
				}
			case key.Event:
				if event.Code == key.CodeEscape && event.Direction == key.DirPress {
					return
				}
			}
		}
	})
}

func startGUIInstall(window screen.Window, capsule bootstrap.Capsule, state *guiState) {
	state.Lock()
	state.phase = phaseInstalling
	state.status = "Preparing cpak"
	state.frame = 0
	state.Unlock()
	window.Send(guiUpdate{})

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
				window.Send(guiUpdate{})
			case <-stop:
				return
			}
		}
	}()

	go func() {
		err := install(capsule, func(message string) {
			message = guiProgressLabel(message, capsule.Metadata.Name)
			if message == "" {
				return
			}
			state.Lock()
			state.status = message
			state.Unlock()
			window.Send(guiUpdate{})
		})
		close(stop)
		state.Lock()
		if err != nil {
			state.phase = phaseFailed
			state.status = err.Error()
		} else {
			state.phase = phaseDone
			state.status = capsule.Metadata.Name + " is installed"
		}
		state.Unlock()
		window.Send(guiUpdate{})
	}()
}

func renderGUI(s screen.Screen, window screen.Window, dimensions size.Event, button image.Rectangle, metadata bootstrap.Metadata, icon image.Image, state *guiState) {
	width, height := dimensions.WidthPx, dimensions.HeightPx
	if width <= 0 || height <= 0 {
		return
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(card), image.Point{}, draw.Src)
	drawRectOutline(canvas, canvas.Bounds(), cardLine)

	draw.Draw(canvas, image.Rect(width/2-54, 52, width/2+54, 160), icon, image.Point{}, draw.Over)
	drawCentered(canvas, metadata.Name, width/2, 208, 28, true, textMain)
	drawWrapped(canvas, metadata.Description, image.Rect(48, 232, width-48, 300), 16, textMuted)
	drawCentered(canvas, metadata.Origin, width/2, 332, 13, false, textMuted)

	state.Lock()
	phase, status, hovered, closeHovered, permissionsHovered, permissionsOpen, frame := state.phase, state.status, state.hovered, state.closeHovered, state.permissionsHovered, state.permissionsOpen, state.frame
	state.Unlock()
	drawCloseButton(canvas, closeRect(width), closeHovered)
	if len(metadata.Permissions) > 0 {
		drawPermissions(canvas, width, metadata.Permissions, permissionsHovered, permissionsOpen)
	}
	statusColor := textMuted
	if phase == phaseDone {
		statusColor = good
	} else if phase == phaseFailed {
		statusColor = bad
	}
	drawCenteredFitted(canvas, status, width/2, button.Min.Y-38, width-72, 14, false, statusColor)
	if phase == phaseInstalling {
		drawProgress(canvas, image.Rect(button.Min.X, button.Min.Y-20, button.Max.X, button.Min.Y-14), frame)
	}

	buttonColor := primary
	if hovered && phase != phaseInstalling {
		buttonColor = primaryHot
	}
	if phase == phaseInstalling {
		buttonColor = color.RGBA{0x2a, 0x47, 0x7f, 0xff}
	}
	drawRounded(canvas, button, 14, buttonColor)
	label := "Install"
	if phase == phaseInstalling {
		label = "Installing..."
	} else if phase == phaseDone {
		label = "Close"
	} else if phase == phaseFailed {
		label = "Try again"
	}
	drawCentered(canvas, label, width/2, button.Min.Y+33, 16, true, textMain)

	buffer, err := s.NewBuffer(image.Pt(width, height))
	if err != nil {
		return
	}
	defer buffer.Release()
	draw.Draw(buffer.RGBA(), buffer.Bounds(), canvas, image.Point{}, draw.Src)
	window.Upload(image.Point{}, buffer, buffer.Bounds())
	window.Publish()
}

func buttonRect(width, height int) image.Rectangle {
	buttonWidth := 268
	left := (width - buttonWidth) / 2
	return image.Rect(left, height-90, left+buttonWidth, height-38)
}

func closeRect(width int) image.Rectangle {
	return image.Rect(width-48, 16, width-16, 48)
}

func permissionsRect(width int) image.Rectangle {
	return image.Rect(40, 350, width-40, 398)
}

func expandedWindowHeight(permissionCount int) int {
	return 540 + ((permissionCount+1)/2)*46
}

func drawPermissions(target *image.RGBA, width int, permissions []bootstrap.Permission, hovered, open bool) {
	bounds := permissionsRect(width)
	fill := color.RGBA{0x11, 0x1a, 0x2b, 0xff}
	if hovered {
		fill = color.RGBA{0x1d, 0x2a, 0x43, 0xff}
	}
	drawRounded(target, bounds, 12, fill)
	drawText(target, "Permissions", bounds.Min.X+16, bounds.Min.Y+29, 14, true, textMain)
	drawText(target, fmt.Sprintf("%d", len(permissions)), bounds.Max.X-54, bounds.Min.Y+29, 13, false, textMuted)
	drawChevron(target, image.Pt(bounds.Max.X-23, bounds.Min.Y+24), open)
	if !open {
		return
	}

	const gap = 10
	columnWidth := (bounds.Dx() - gap) / 2
	for index, permission := range permissions {
		column := index % 2
		row := index / 2
		left := bounds.Min.X + column*(columnWidth+gap)
		top := bounds.Max.Y + 10 + row*46
		item := image.Rect(left, top, left+columnWidth, top+36)
		drawRounded(target, item, 9, fill)
		drawTextFitted(target, permission.Name+": "+permission.Detail, item.Min.X+11, item.Min.Y+23, item.Dx()-22, 12, false, textMuted)
	}
}

func drawChevron(target *image.RGBA, center image.Point, open bool) {
	for offset := -1; offset <= 1; offset++ {
		for i := 0; i < 6; i++ {
			if open {
				target.Set(center.X-5+i, center.Y+3-i+offset, textMuted)
				target.Set(center.X+i, center.Y-2+i+offset, textMuted)
			} else {
				target.Set(center.X-2+i+offset, center.Y-5+i, textMuted)
				target.Set(center.X+3-i+offset, center.Y+i, textMuted)
			}
		}
	}
}

func drawCloseButton(target *image.RGBA, bounds image.Rectangle, hovered bool) {
	fill := color.RGBA{0x1d, 0x2a, 0x43, 0xff}
	if hovered {
		fill = color.RGBA{0x2a, 0x3b, 0x5c, 0xff}
	}
	drawRounded(target, bounds, bounds.Dx()/2, fill)
	for offset := -1; offset <= 1; offset++ {
		for i := 0; i < 10; i++ {
			target.Set(bounds.Min.X+11+i, bounds.Min.Y+11+i+offset, textMain)
			target.Set(bounds.Max.X-12-i, bounds.Min.Y+11+i+offset, textMain)
		}
	}
}

func renderIcon(encoded, name string, size int) image.Image {
	iconImage := image.NewRGBA(image.Rect(0, 0, size, size))
	if encoded != "" {
		icon, err := oksvg.ReadIconStream(strings.NewReader(encoded), oksvg.IgnoreErrorMode)
		if err == nil {
			icon.SetTarget(0, 0, float64(size), float64(size))
			scanner := rasterx.NewScannerGV(size, size, iconImage, iconImage.Bounds())
			raster := rasterx.NewDasher(size, size, scanner)
			icon.Draw(raster, 1)
			return iconImage
		}
	}
	drawRounded(iconImage, iconImage.Bounds(), 24, primary)
	letter := "?"
	if runes := []rune(name); len(runes) > 0 {
		letter = strings.ToUpper(string(runes[0]))
	}
	drawCentered(iconImage, letter, size/2, size/2+19, 48, true, textMain)
	return iconImage
}

func drawProgress(target *image.RGBA, bounds image.Rectangle, frame int) {
	drawRounded(target, bounds, 3, cardLine)
	span := bounds.Dx() / 3
	travel := bounds.Dx() + span
	left := bounds.Min.X + (frame*7)%travel - span
	active := image.Rect(left, bounds.Min.Y, left+span, bounds.Max.Y).Intersect(bounds)
	drawRounded(target, active, 3, primary)
}

func drawRounded(target *image.RGBA, bounds image.Rectangle, radius int, fill color.Color) {
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

func drawRectOutline(target *image.RGBA, bounds image.Rectangle, fill color.Color) {
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Min.Y+1), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Max.Y-1, bounds.Max.X, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Min.X+1, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
	draw.Draw(target, image.Rect(bounds.Max.X-1, bounds.Min.Y, bounds.Max.X, bounds.Max.Y), image.NewUniform(fill), image.Point{}, draw.Src)
}

func drawCentered(target draw.Image, value string, centerX, baseline, size int, bold bool, fill color.Color) {
	face := fontFace(size, bold)
	width := font.MeasureString(face, value).Round()
	drawer := font.Drawer{Dst: target, Src: image.NewUniform(fill), Face: face, Dot: fixed.P(centerX-width/2, baseline)}
	drawer.DrawString(value)
}

func drawText(target draw.Image, value string, x, baseline, size int, bold bool, fill color.Color) {
	drawer := font.Drawer{Dst: target, Src: image.NewUniform(fill), Face: fontFace(size, bold), Dot: fixed.P(x, baseline)}
	drawer.DrawString(value)
}

func drawTextFitted(target draw.Image, value string, x, baseline, maxWidth, size int, bold bool, fill color.Color) {
	value = fitText(value, maxWidth, fontFace(size, bold))
	drawText(target, value, x, baseline, size, bold, fill)
}

func drawCenteredFitted(target draw.Image, value string, centerX, baseline, maxWidth, size int, bold bool, fill color.Color) {
	face := fontFace(size, bold)
	value = fitText(value, maxWidth, face)
	drawCentered(target, value, centerX, baseline, size, bold, fill)
}

func fitText(value string, maxWidth int, face font.Face) string {
	value = strings.Join(strings.Fields(value), " ")
	if font.MeasureString(face, value).Round() <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 3 && font.MeasureString(face, string(runes)+"...").Round() > maxWidth {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func drawWrapped(target draw.Image, value string, bounds image.Rectangle, size int, fill color.Color) {
	face := fontFace(size, false)
	words := strings.Fields(value)
	lines := []string{}
	line := ""
	for _, word := range words {
		candidate := strings.TrimSpace(line + " " + word)
		if line != "" && font.MeasureString(face, candidate).Round() > bounds.Dx() {
			lines = append(lines, line)
			line = word
		} else {
			line = candidate
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	baseline := bounds.Min.Y + size
	for _, line := range lines {
		drawCentered(target, line, bounds.Min.X+bounds.Dx()/2, baseline, size, false, fill)
		baseline += size + 7
		if baseline > bounds.Max.Y {
			break
		}
	}
}

func fontFace(size int, bold bool) font.Face {
	key := struct {
		size int
		bold bool
	}{size: size, bold: bold}
	if cached, ok := faceCache.Load(key); ok {
		return cached.(font.Face)
	}
	data := goregular.TTF
	if bold {
		data = gobold.TTF
	}
	parsed, _ := opentype.Parse(data)
	face, _ := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 96, Hinting: font.HintingFull})
	faceCache.Store(key, face)
	return face
}

func guiProgressLabel(message, name string) string {
	message = strings.TrimSpace(message)
	switch {
	case message == "cpak is ready", strings.HasPrefix(message, "Installed cpak"):
		return "cpak is ready"
	case strings.HasPrefix(message, "Resolving "):
		return "Preparing " + name
	case strings.Contains(message, "Downloading"):
		return "Downloading " + name
	case strings.Contains(message, "Extracting"), strings.Contains(message, "Installing"):
		return "Installing " + name
	default:
		return ""
	}
}
