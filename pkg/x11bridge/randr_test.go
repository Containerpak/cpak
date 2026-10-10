/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package x11bridge

import (
	"os"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/randr"
	"github.com/jezek/xgb/xproto"
)

func TestResizeRootShrinksAfterGrowing(t *testing.T) {
	display := os.Getenv("CPAK_TEST_RANDR_DISPLAY")
	if display == "" {
		t.Skip("requires a dedicated RandR display")
	}
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = randr.Init(c); err != nil {
		t.Fatal(err)
	}
	root := xproto.Setup(c).DefaultScreen(c).Root
	b := &broker{nested: &endpoint{connection: c, root: root}, randr: true}
	resources, err := randr.GetScreenResourcesCurrent(c, root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]uint16{{1280, 960}, {1024, 768}, {1280, 720}, {1024, 768}, {1280, 960}, {1024, 768}} {
		mode, ok := smallestMode(resources.Modes, size[0], size[1])
		if !ok {
			t.Fatalf("display has no mode for %dx%d", size[0], size[1])
		}
		b.resizeRoot(size[0], size[1])
		g, err := xproto.GetGeometry(c, xproto.Drawable(root)).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if g.Width != mode.Width || g.Height != mode.Height {
			t.Fatalf("resize to %dx%d: root stayed %dx%d, expected %dx%d", size[0], size[1], g.Width, g.Height, mode.Width, mode.Height)
		}
		t.Logf("requested %dx%d, root %dx%d", size[0], size[1], g.Width, g.Height)
	}
}
