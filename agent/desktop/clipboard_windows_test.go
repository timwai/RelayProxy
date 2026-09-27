//go:build windows

package desktop

import (
	"image"
	"image/color"
	"testing"

	"relayproxy/internal/protocol"
)

func TestWindowsClipboardUTF16Sizing(t *testing.T) {
	text := "RelayProxy 中文 😀"
	normalized, err := validateClipboardText(text)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != text {
		t.Fatalf("normalized=%q", normalized)
	}
	if len([]byte(text)) > protocol.MaxDesktopClipboardBytes {
		t.Fatal("test text unexpectedly exceeds clipboard limit")
	}
}

func TestWindowsClipboardDIBRoundTrip(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	want := []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
		{R: 0, G: 255, B: 0, A: 255},
		{R: 0, G: 0, B: 255, A: 255},
		{R: 10, G: 20, B: 30, A: 255},
		{R: 40, G: 50, B: 60, A: 255},
		{R: 70, G: 80, B: 90, A: 255},
	}
	for i, c := range want {
		src.SetRGBA(i%3, i/3, c)
	}
	raw, err := encodeWindowsClipboardDIB(src)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeWindowsClipboardDIB(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(*image.RGBA)
	if !ok {
		t.Fatalf("decoded type=%T", decoded)
	}
	for i, c := range want {
		if actual := got.RGBAAt(i%3, i/3); actual != c {
			t.Fatalf("pixel %d=%+v want=%+v", i, actual, c)
		}
	}
}
