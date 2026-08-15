/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"image"
	"image/draw"
	"sync"
	"time"

	"golang.org/x/exp/shiny/driver"
	"golang.org/x/exp/shiny/screen"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
)

type progressState struct {
	sync.Mutex
	update  ProgressUpdate
	frame   int
	done    bool
	err     error
	palette desktopPalette
}

type progressEvent struct{}

func progressBuiltin(request ProgressRequest, updates <-chan ProgressUpdate, done <-chan error) error {
	var windowErr error
	driver.Main(func(display screen.Screen) {
		const width, height = 520, 310
		decoration := captureDesktopWindows()
		window, err := display.NewWindow(&screen.NewWindowOptions{Width: width, Height: height, Title: request.Title})
		if err != nil {
			decoration.Close()
			windowErr = err
			return
		}
		decoration.Apply(request.Title, request.IconPNG)
		defer window.Release()
		frame := newDesktopFrame(request.Title)
		if frame != nil {
			defer frame.Close()
		}
		state := &progressState{update: ProgressUpdate{Message: request.Detail}, palette: currentDesktopPalette()}
		stop := make(chan struct{})
		defer close(stop)
		go monitorBuiltinProgress(window, state, updates, done, stop)
		var dimensions size.Event
		for {
			event := window.NextEvent()
			switch event := event.(type) {
			case lifecycle.Event:
				if event.To == lifecycle.StageDead {
					for {
						state.Lock()
						finished := state.done
						result := state.err
						state.Unlock()
						if finished {
							windowErr = result
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					return
				}
			case size.Event:
				dimensions = event
				window.Send(paint.Event{})
			case paint.Event, progressEvent:
				renderBuiltinProgress(display, window, dimensions, request, state)
				state.Lock()
				finished := state.done
				result := state.err
				state.Unlock()
				if finished {
					windowErr = result
					return
				}
			}
		}
	})
	return windowErr
}

func monitorBuiltinProgress(window screen.Window, state *progressState, updates <-chan ProgressUpdate, done <-chan error, stop <-chan struct{}) {
	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case update := <-updates:
			state.Lock()
			state.update = update
			state.Unlock()
			window.Send(progressEvent{})
		case err := <-done:
			state.Lock()
			state.done = true
			state.err = err
			state.Unlock()
			window.Send(progressEvent{})
			return
		case <-ticker.C:
			state.Lock()
			state.frame++
			state.Unlock()
			window.Send(progressEvent{})
		case <-stop:
			return
		}
	}
}

func renderBuiltinProgress(display screen.Screen, window screen.Window, dimensions size.Event, request ProgressRequest, state *progressState) {
	width, height := dimensions.WidthPx, dimensions.HeightPx
	if width <= 0 || height <= 0 {
		return
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	palette := state.palette
	drawGrantBackdrop(canvas, palette)
	drawUpdateOutline(canvas, canvas.Bounds(), palette.line)
	drawGrantBrand(canvas, palette, brandIconPNG)
	icon := renderUpdateIcon(request.IconPNG, 64)
	draw.Draw(canvas, image.Rect(width/2-32, 54, width/2+32, 118), icon, image.Point{}, draw.Over)
	drawUpdateCentered(canvas, request.Heading, width/2, 158, 22, true, palette.text)
	drawUpdateCenteredFitted(canvas, request.Detail, width/2, 186, width-80, 14, false, palette.muted)

	state.Lock()
	update, frame := state.update, state.frame
	state.Unlock()
	drawUpdateCenteredFitted(canvas, update.Message, width/2, 230, width-80, 13, false, palette.text)
	bar := image.Rect(64, 256, width-64, 264)
	drawUpdateRounded(canvas, bar, 4, palette.line)
	if update.Total > 0 {
		span := int(int64(bar.Dx()) * update.Current / update.Total)
		if span > bar.Dx() {
			span = bar.Dx()
		}
		drawUpdateRounded(canvas, image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+span, bar.Max.Y), 4, palette.accent)
	} else {
		drawUpdateProgressColor(canvas, bar, frame, palette.line, palette.accent)
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
