/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package desktopui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestSummarizeNotesRemovesMarkdownAndRawLinks(t *testing.T) {
	notes := "## What's Changed\n* **Fix the updater** by [@maintainer](https://github.com/maintainer) in https://github.com/Containerpak/cpak/pull/42\n**Full Changelog**: https://github.com/Containerpak/cpak/compare/v1...v2"
	got := summarizeNotes(notes, 360)
	if got != "Fix the updater by @maintainer in #42" {
		t.Fatalf("unexpected release notes: %q", got)
	}
	if strings.ContainsAny(got, "*[]()") || strings.Contains(got, "https://") {
		t.Fatalf("release notes contain raw markup: %q", got)
	}
}

func TestRenderUpdateIconCentersPNG(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 6))
	for y := source.Bounds().Min.Y; y < source.Bounds().Max.Y; y++ {
		for x := source.Bounds().Min.X; x < source.Bounds().Max.X; x++ {
			source.Set(x, y, color.RGBA{0x42, 0x7a, 0xf4, 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	icon := renderUpdateIcon(encoded.Bytes(), 10)
	if got := color.RGBAModel.Convert(icon.At(3, 2)).(color.RGBA); got.B != 0xf4 {
		t.Fatalf("rendered icon has the wrong color: %#v", got)
	}
	if got := color.RGBAModel.Convert(icon.At(0, 0)).(color.RGBA); got.A != 0 {
		t.Fatalf("rendered icon is not centered: %#v", got)
	}
}
