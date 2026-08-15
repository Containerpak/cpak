/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"errors"
	"image"
	"testing"
)

func TestDesktopParentWindow(t *testing.T) {
	window, ok := desktopParentWindow("x11:2a")
	if !ok || window != 42 {
		t.Fatalf("window: %d, ok: %t", window, ok)
	}
	for _, parent := range []string{"", "wayland:token", "x11:", "x11:nope", "x11:0"} {
		if _, ok := desktopParentWindow(parent); ok {
			t.Fatalf("accepted invalid parent %q", parent)
		}
	}
}

func TestFileGrantContinueHitUsesActualWindowHeight(t *testing.T) {
	request := FilePickerRequest{OfferContainingFolder: true, OfferPersistent: true}
	button := fileGrantAction(1120, 860, 1)
	point := image.Pt(button.Min.X+1, button.Min.Y+1)
	if control := fileGrantControlAt(point, 1120, 860, request); control != 3 {
		t.Fatalf("control: %d", control)
	}
}

func TestFileGrantPromptCloseDeniesAccess(t *testing.T) {
	result := FilePickerResult{Path: "/home/test/document.pdf", Persistent: true, ContainingFolder: true}
	got, err := fileGrantPromptResult(result, false, nil)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("error: %v", err)
	}
	if got.Path != "" || len(got.Paths) != 0 || got.Persistent || got.ContainingFolder {
		t.Fatalf("cancelled result retained access: %#v", got)
	}
}

func TestFileGrantPromptAcceptReturnsSelection(t *testing.T) {
	result := FilePickerResult{Path: "/home/test/document.pdf", Persistent: true, ContainingFolder: true}
	got, err := fileGrantPromptResult(result, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != result.Path || !got.Persistent || !got.ContainingFolder {
		t.Fatalf("result: %#v", got)
	}
}
