package viewer

import (
	"errors"
	"fmt"

	"relayproxy/internal/protocol"
)

var ErrUnavailable = errors.New("native desktop viewer unavailable")

type Viewport struct {
	Width  int
	Height int
}

func (v Viewport) Valid() bool {
	return v.Width > 0 && v.Height > 0
}

type Config struct {
	Title          string
	Width          int
	Height         int
	ViewportWidth  int
	ViewportHeight int
	OnInput        func(protocol.DesktopInputEvent)
	OnViewport     func(Viewport)
}

type Frame struct {
	BGRA   []byte
	Width  int
	Height int
	Stride int
}

type D3D11Frame struct {
	Resource    uintptr
	Subresource uint32
	Width       int
	Height      int
}

type CursorOverlay struct {
	State  protocol.DesktopCursorState
	Bitmap CursorBitmap
}

func (f D3D11Frame) Validate() error {
	if f.Resource == 0 || f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("%w: invalid D3D11 frame", ErrUnavailable)
	}
	return nil
}

func (f Frame) Validate() error {
	if f.Width <= 0 || f.Height <= 0 {
		return fmt.Errorf("%w: invalid frame dimensions", ErrUnavailable)
	}
	if f.Stride < f.Width*4 || len(f.BGRA) < f.Stride*f.Height {
		return fmt.Errorf("%w: BGRA frame buffer is too small", ErrUnavailable)
	}
	return nil
}

type Native interface {
	Submit(Frame) error
	SubmitD3D11(D3D11Frame) error
	D3D11Device() uintptr
	SupportsGPUCursor() bool
	SetCursor(CursorOverlay) error
	Viewport() Viewport
	Focus()
	Done() <-chan struct{}
	Close() error
}
