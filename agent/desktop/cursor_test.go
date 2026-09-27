package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestCursorStateChangedIgnoresPNGBytes(t *testing.T) {
	previous := protocol.DesktopCursorState{
		X: 10, Y: 20, ScreenWidth: 1920, ScreenHeight: 1080, Visible: true,
		CursorID: "arrow", Width: 32, Height: 32, HotspotX: 1, HotspotY: 2, PNG: []byte{1},
	}
	next := previous
	next.PNG = []byte{2, 3}
	if cursorStateChanged(previous, next) {
		t.Fatal("shape bytes alone should not emit another cursor update")
	}
	next.X++
	if !cursorStateChanged(previous, next) {
		t.Fatal("cursor movement was not detected")
	}
}

func TestCursorUpdateSendsShapeOnlyOnIDChange(t *testing.T) {
	previous := protocol.DesktopCursorState{CursorID: "a"}
	next := protocol.DesktopCursorState{CursorID: "a", PNG: []byte{1, 2, 3}}
	update := cursorUpdate(previous, next, 7)
	if update.Sequence != 7 || len(update.PNG) != 0 {
		t.Fatalf("same-shape update=%+v", update)
	}

	next.CursorID = "b"
	update = cursorUpdate(previous, next, 8)
	if update.Sequence != 8 || len(update.PNG) != 3 {
		t.Fatalf("new-shape update=%+v", update)
	}
	update.PNG[0] = 9
	if next.PNG[0] != 1 {
		t.Fatal("cursor PNG was not copied")
	}
}
