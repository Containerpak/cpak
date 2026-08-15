/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"sync"

	"github.com/mirkobrombin/cpak/pkg/filegrant"
	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/mobile/event/key"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/mouse"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
)

type GrantManagerRequest struct {
	Application string
	Grants      []filegrant.Grant
	Revoke      func(filegrant.Grant) error
}

type grantManagerState struct {
	sync.Mutex
	grants       []filegrant.Grant
	hovered      int
	offset       int
	closeHovered bool
	status       string
	busy         bool
	palette      desktopPalette
}

type grantManagerEvent struct{}

func ManageFileGrants(request GrantManagerRequest) error {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("grant manager is unavailable in a headless session")
	}
	if request.Revoke == nil {
		return errors.New("grant manager revoke action is required")
	}
	var windowErr error
	driver.Main(func(display screen.Screen) {
		const width = 680
		height := grantWindowHeight(len(request.Grants))
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
			defer frame.Close()
		}
		state := &grantManagerState{grants: append([]filegrant.Grant{}, request.Grants...), hovered: -1, palette: currentDesktopPalette()}
		var dimensions size.Event
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
			case paint.Event, grantManagerEvent:
				renderGrantManager(display, window, dimensions, request.Application, state)
			case mouse.Event:
				point := image.Pt(int(event.X), int(event.Y))
				closeHovered := point.In(updateClose(dimensions.WidthPx))
				if closeHovered && event.Button == mouse.ButtonLeft && event.Direction == mouse.DirPress {
					return
				}
				state.Lock()
				previousOffset := state.offset
				if event.Direction == mouse.DirStep || event.Direction == mouse.DirPress {
					switch event.Button {
					case mouse.ButtonWheelUp:
						if state.offset > 0 {
							state.offset--
						}
					case mouse.ButtonWheelDown:
						if state.offset < maxGrantOffset(len(state.grants)) {
							state.offset++
						}
					}
				}
				hovered := grantButtonAt(point, dimensions.WidthPx, len(state.grants), state.offset)
				changed := state.hovered != hovered || state.closeHovered != closeHovered || state.offset != previousOffset
				state.hovered = hovered
				state.closeHovered = closeHovered
				busy := state.busy
				state.Unlock()
				if changed {
					window.Send(paint.Event{})
				}
				if event.Button == mouse.ButtonLeft && event.Direction == mouse.DirPress && hovered >= 0 && !busy {
					revokeGrant(window, state, hovered, request.Revoke)
				} else if event.Button == mouse.ButtonLeft && event.Direction == mouse.DirPress && point.Y < 54 && frame != nil {
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

func grantWindowHeight(count int) int {
	if count < 1 {
		return 300
	}
	if count > 7 {
		count = 7
	}
	return 190 + count*66
}

func revokeGrant(window screen.Window, state *grantManagerState, index int, action func(filegrant.Grant) error) {
	state.Lock()
	if index >= len(state.grants) {
		state.Unlock()
		return
	}
	grant := state.grants[index]
	state.busy = true
	state.status = "Revoking access..."
	state.Unlock()
	window.Send(grantManagerEvent{})
	go func() {
		err := action(grant)
		state.Lock()
		state.busy = false
		if err != nil {
			state.status = err.Error()
		} else {
			state.grants = append(state.grants[:index], state.grants[index+1:]...)
			if state.offset > maxGrantOffset(len(state.grants)) {
				state.offset = maxGrantOffset(len(state.grants))
			}
			state.hovered = -1
			state.status = "Access revoked"
		}
		state.Unlock()
		window.Send(grantManagerEvent{})
	}()
}

func renderGrantManager(display screen.Screen, window screen.Window, dimensions size.Event, application string, state *grantManagerState) {
	width, height := dimensions.WidthPx, dimensions.HeightPx
	if width <= 0 || height <= 0 {
		return
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	state.Lock()
	grants := append([]filegrant.Grant{}, state.grants...)
	hovered, offset, closeHovered, status, busy := state.hovered, state.offset, state.closeHovered, state.status, state.busy
	palette := state.palette
	state.Unlock()
	drawGrantBackdrop(canvas, palette)
	drawUpdateOutline(canvas, canvas.Bounds(), palette.line)
	drawGrantBrand(canvas, palette, brandIconPNG)
	drawGrantClose(canvas, updateClose(width), closeHovered, palette)
	drawUpdateCentered(canvas, "File access", width/2, 48, 20, true, palette.text)
	drawUpdateCenteredFitted(canvas, application, width/2, 78, width-120, 14, false, palette.muted)

	if len(grants) == 0 {
		drawUpdateCentered(canvas, "No persistent file access", width/2, height/2+12, 16, false, palette.muted)
	}
	end := offset + 7
	if end > len(grants) {
		end = len(grants)
	}
	for index := offset; index < end; index++ {
		grant := grants[index]
		row := grantRow(width, index-offset)
		drawUpdateRounded(canvas, row, 12, palette.surface)
		drawUpdateCenteredFitted(canvas, filepath.Base(grant.Selection), row.Min.X+150, row.Min.Y+24, 270, 14, true, palette.text)
		drawUpdateCenteredFitted(canvas, grant.Selection, row.Min.X+185, row.Min.Y+47, 340, 11, false, palette.muted)
		button := grantButton(width, index-offset)
		buttonColor := palette.surfaceStrong
		if hovered == index && !busy {
			buttonColor = color.RGBA{0x72, 0x32, 0x45, 0xff}
		}
		drawUpdateRounded(canvas, button, 10, buttonColor)
		drawUpdateCentered(canvas, "Revoke", button.Min.X+button.Dx()/2, button.Min.Y+27, 12, true, palette.text)
	}
	if len(grants) > 7 && status == "" {
		drawUpdateCentered(canvas, fmt.Sprintf("%d-%d of %d", offset+1, end, len(grants)), width/2, height-24, 12, false, palette.muted)
	}
	if status != "" {
		drawUpdateCenteredFitted(canvas, status, width/2, height-24, width-80, 12, false, palette.muted)
	}
	buffer, err := display.NewBuffer(image.Pt(width, height))
	if err != nil {
		return
	}
	defer buffer.Release()
	draw.Draw(buffer.RGBA(), buffer.Bounds(), canvas, image.Point{}, draw.Src)
	window.Upload(image.Point{}, buffer, buffer.Bounds())
	window.Publish()
}

func grantRow(width, index int) image.Rectangle {
	return image.Rect(34, 102+index*66, width-34, 158+index*66)
}

func grantButton(width, index int) image.Rectangle {
	row := grantRow(width, index)
	return image.Rect(row.Max.X-112, row.Min.Y+9, row.Max.X-12, row.Max.Y-9)
}

func grantButtonAt(point image.Point, width, count, offset int) int {
	visible := count - offset
	if visible > 7 {
		visible = 7
	}
	for index := 0; index < visible; index++ {
		if point.In(grantButton(width, index)) {
			return offset + index
		}
	}
	return -1
}

func maxGrantOffset(count int) int {
	if count <= 7 {
		return 0
	}
	return count - 7
}
