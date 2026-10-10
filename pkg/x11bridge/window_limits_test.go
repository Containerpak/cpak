/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package x11bridge

import (
	"os"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func TestWindowSizeLimits(t *testing.T) {
	display := os.Getenv("CPAK_TEST_RANDR_DISPLAY")
	if display == "" {
		t.Skip("requires a dedicated X11 display")
	}
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	screen := xproto.Setup(c).DefaultScreen(c)
	root, err := xproto.NewWindowId(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(c, screen.RootDepth, root, screen.Root, 0, 0, 1280, 960, 0, xproto.WindowClassInputOutput, screen.RootVisual, xproto.CwOverrideRedirect, []uint32{1}).Check(); err != nil {
		t.Fatal(err)
	}
	defer xproto.DestroyWindow(c, root)
	b := &broker{nested: &endpoint{connection: c, root: root}}
	for _, tc := range []struct {
		name          string
		hints         []uint32
		width, height uint16
		configure     bool
	}{
		{"resizable", nil, 1280, 960, false},
		{"maximum", []uint32{32, 0, 0, 0, 0, 0, 0, 800, 600}, 800, 600, false},
		{"small maximum", []uint32{32, 0, 0, 0, 0, 0, 0, 160, 20}, 160, 20, true},
		{"truncated", []uint32{32}, 1280, 960, false},
		{"negative maximum", []uint32{32, 0, 0, 0, 0, 0, 0, 0xffffffff, 600}, 1280, 960, false},
		{"unflagged maximum", []uint32{0, 0, 0, 0, 0, 0, 0, 800, 600}, 1280, 960, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := xproto.NewWindowId(c)
			if err != nil {
				t.Fatal(err)
			}
			if err = xproto.CreateWindowChecked(c, screen.RootDepth, w, root, 0, 0, 1024, 768, 0, xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
				t.Fatal(err)
			}
			defer xproto.DestroyWindow(c, w)
			if tc.hints != nil {
				setProperty32(c, w, xproto.AtomWmNormalHints, xproto.AtomWmSizeHints, tc.hints...)
			}
			if tc.configure {
				b.configureWindow(xproto.ConfigureRequestEvent{Window: w, Width: tc.width, Height: tc.height, ValueMask: xproto.ConfigWindowWidth | xproto.ConfigWindowHeight})
			} else {
				b.fitWindow(w)
			}
			g, err := xproto.GetGeometry(c, xproto.Drawable(w)).Reply()
			if err != nil {
				t.Fatal(err)
			}
			if g.Width != tc.width || g.Height != tc.height {
				t.Fatalf("window is %dx%d, expected %dx%d", g.Width, g.Height, tc.width, tc.height)
			}
		})
	}
}
