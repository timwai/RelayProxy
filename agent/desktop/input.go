package desktop

import (
	"context"
	"errors"
	"fmt"

	"relayproxy/internal/protocol"
)

type InputSink interface {
	ApplyInput(context.Context, protocol.DesktopInputEvent) error
	ReleaseAll() error
}

func ValidateDesktopInputEvent(event protocol.DesktopInputEvent) error {
	switch event.Kind {
	case protocol.DesktopInputKeyDown, protocol.DesktopInputKeyUp:
		if event.VirtualKey == 0 {
			return errors.New("Relay Desktop keyboard event requires a virtual key")
		}
	case protocol.DesktopInputMouseMove:
		return nil
	case protocol.DesktopInputMouseDown, protocol.DesktopInputMouseUp:
		switch event.Button {
		case protocol.DesktopMouseButtonLeft, protocol.DesktopMouseButtonRight,
			protocol.DesktopMouseButtonMiddle, protocol.DesktopMouseButtonX1, protocol.DesktopMouseButtonX2:
		default:
			return fmt.Errorf("unsupported Relay Desktop mouse button %q", event.Button)
		}
	case protocol.DesktopInputMouseWheel:
		if event.WheelDelta == 0 || event.WheelDelta < -12000 || event.WheelDelta > 12000 {
			return errors.New("Relay Desktop mouse wheel delta is invalid")
		}
	default:
		return fmt.Errorf("unsupported Relay Desktop input kind %q", event.Kind)
	}
	return nil
}
