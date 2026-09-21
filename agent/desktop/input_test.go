package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestValidateDesktopInputEvent(t *testing.T) {
	valid := []protocol.DesktopInputEvent{
		{Kind: protocol.DesktopInputKeyDown, VirtualKey: 0x41},
		{Kind: protocol.DesktopInputKeyUp, VirtualKey: 0x41},
		{Kind: protocol.DesktopInputMouseMove, X: 65535, Y: 32768},
		{Kind: protocol.DesktopInputMouseDown, Button: protocol.DesktopMouseButtonLeft},
		{Kind: protocol.DesktopInputMouseUp, Button: protocol.DesktopMouseButtonX2},
		{Kind: protocol.DesktopInputMouseWheel, WheelDelta: 120},
	}
	for _, event := range valid {
		if err := ValidateDesktopInputEvent(event); err != nil {
			t.Fatalf("valid event %+v rejected: %v", event, err)
		}
	}

	invalid := []protocol.DesktopInputEvent{
		{Kind: protocol.DesktopInputKeyDown},
		{Kind: protocol.DesktopInputMouseDown, Button: "button9"},
		{Kind: protocol.DesktopInputMouseWheel},
		{Kind: protocol.DesktopInputMouseWheel, WheelDelta: 12001},
		{Kind: "unknown"},
	}
	for _, event := range invalid {
		if err := ValidateDesktopInputEvent(event); err == nil {
			t.Fatalf("invalid event %+v was accepted", event)
		}
	}
}
