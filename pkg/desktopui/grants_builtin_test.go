/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"image"
	"testing"
)

func TestGrantManagerScrollsThroughEveryPersistentGrant(t *testing.T) {
	if maxGrantOffset(6) != 0 || maxGrantOffset(10) != 3 {
		t.Fatal("grant manager offset is invalid")
	}
	button := grantButton(680, 0)
	point := image.Pt(button.Min.X+1, button.Min.Y+1)
	if index := grantButtonAt(point, 680, 10, 3); index != 3 {
		t.Fatalf("grant manager selected index %d, want 3", index)
	}
}
