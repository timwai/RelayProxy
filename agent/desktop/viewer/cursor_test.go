package viewer

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestCompositeCursorBGRA(t *testing.T) {
	base := make([]byte, 4*4*4)
	for i := 3; i < len(base); i += 4 {
		base[i] = 0xff
	}
	shape := CursorBitmap{
		ID: "cursor",
		Pix: []byte{
			255, 0, 0, 255,
			0, 255, 0, 128,
			0, 0, 255, 255,
			255, 255, 255, 0,
		},
		Width: 2, Height: 2, Stride: 8,
	}
	state := protocol.DesktopCursorState{
		Visible: true, X: 1, Y: 1,
		ScreenWidth: 4, ScreenHeight: 4,
		Width: 2, Height: 2,
	}
	got, err := CompositeCursorBGRA(base, 4, 4, 16, state, shape, nil)
	if err != nil {
		t.Fatal(err)
	}
	red := 1*16 + 1*4
	if got[red] != 0 || got[red+1] != 0 || got[red+2] != 255 || got[red+3] != 255 {
		t.Fatalf("red cursor pixel=%v", got[red:red+4])
	}
	blue := 2*16 + 1*4
	if got[blue] != 255 || got[blue+1] != 0 || got[blue+2] != 0 {
		t.Fatalf("blue cursor pixel=%v", got[blue:blue+4])
	}
}

func TestCompositeCursorBGRACopiesInvisibleFrame(t *testing.T) {
	base := []byte{1, 2, 3, 255, 4, 5, 6, 255}
	got, err := CompositeCursorBGRA(base, 2, 1, 8, protocol.DesktopCursorState{}, CursorBitmap{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range base {
		if got[i] != base[i] {
			t.Fatalf("byte %d=%d want %d", i, got[i], base[i])
		}
	}
}


func TestCursorRectsClipsAtFrameEdge(t *testing.T) {
	shape := CursorBitmap{Width: 20, Height: 10}
	state := protocol.DesktopCursorState{
		Visible: true,
		X: 2, Y: 3,
		HotspotX: 5, HotspotY: 4,
		ScreenWidth: 100, ScreenHeight: 100,
		Width: 20, Height: 10,
	}
	source, dest, ok := cursorRects(100, 100, state, shape)
	if !ok {
		t.Fatal("cursor was unexpectedly clipped away")
	}
	if dest.Min.X != 0 || dest.Min.Y != 0 {
		t.Fatalf("dest min=%v want (0,0)", dest.Min)
	}
	if source.Min.X <= 0 || source.Min.Y <= 0 {
		t.Fatalf("source was not clipped with destination: %v", source)
	}
}
