package desktopui

import (
	"image"
	"image/color"
	"testing"

	"github.com/jezek/xgb"
)

func TestEncodeWindowIcon(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 0x11, G: 0x22, B: 0x33, A: 0x44})
	source.SetNRGBA(1, 0, color.NRGBA{R: 0xaa, G: 0xbb, B: 0xcc, A: 0xdd})
	encoded := encodeWindowIcon(source)
	if len(encoded) != 16 {
		t.Fatalf("encoded length = %d", len(encoded))
	}
	if width, height := xgb.Get32(encoded), xgb.Get32(encoded[4:]); width != 2 || height != 1 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
	if first, second := xgb.Get32(encoded[8:]), xgb.Get32(encoded[12:]); first != 0x44112233 || second != 0xddaabbcc {
		t.Fatalf("pixels = %#x %#x", first, second)
	}
}
