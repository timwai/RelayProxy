//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-mswin/screencapture"

	"relayproxy/internal/protocol"
)

var errWindowsGraphicsCaptureUnavailable = errors.New("Windows Graphics Capture is unavailable")

type windowsCaptureFrame struct {
	Pix      []byte
	Width    int
	Height   int
	Stride   int
	Sequence uint64
	At       time.Time
}

func (f windowsCaptureFrame) Valid() bool {
	return f.Width > 0 && f.Height > 0 && f.Stride >= f.Width*4 &&
		len(f.Pix) >= f.Stride*f.Height
}

func (f windowsCaptureFrame) Row(y int) []byte {
	if y < 0 || y >= f.Height || !f.Valid() {
		return nil
	}
	start := y * f.Stride
	return f.Pix[start : start+f.Width*4]
}

type windowsFrameStream interface {
	Frame() (windowsCaptureFrame, bool)
	WaitFrame(context.Context) (windowsCaptureFrame, error)
	Backend() protocol.DesktopCaptureBackend
	Close() error
}

type windowsFrameStreamFactory interface {
	Open(
		context.Context,
		screencapture.Display,
		protocol.DesktopCaptureBackend,
		int,
	) (windowsFrameStream, error)
}

type screencaptureFrameStreamFactory struct{}

func (screencaptureFrameStreamFactory) Open(
	ctx context.Context,
	display screencapture.Display,
	preference protocol.DesktopCaptureBackend,
	maxFPS int,
) (windowsFrameStream, error) {
	preference = normalizedWindowsCaptureBackend(preference)
	if preference != protocol.DesktopCaptureAuto {
		return openConcreteWindowsFrameStream(ctx, display, preference, maxFPS)
	}

	var attempts []error
	for _, candidate := range autoWindowsCaptureBackendOrder(display, windowsWGCAvailable()) {
		stream, err := openConcreteWindowsFrameStream(ctx, display, candidate, maxFPS)
		if err == nil {
			return stream, nil
		}
		attempts = append(attempts, fmt.Errorf("%s: %w", candidate, err))
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if len(attempts) == 0 {
		return nil, errors.New("no Windows capture backend candidates")
	}
	return nil, fmt.Errorf("automatic Windows capture failed: %w", errors.Join(attempts...))
}

func autoWindowsCaptureBackendOrder(
	display screencapture.Display,
	hasWGC bool,
) []protocol.DesktopCaptureBackend {
	backends := make([]protocol.DesktopCaptureBackend, 0, 3)
	if display.Duplicable() {
		backends = append(backends, protocol.DesktopCaptureDXGI)
	}
	if hasWGC {
		backends = append(backends, protocol.DesktopCaptureWGC)
	}
	backends = append(backends, protocol.DesktopCaptureGDI)
	return backends
}

func openConcreteWindowsFrameStream(
	ctx context.Context,
	display screencapture.Display,
	preference protocol.DesktopCaptureBackend,
	maxFPS int,
) (windowsFrameStream, error) {
	if preference == protocol.DesktopCaptureWGC {
		return openWGCFrameStream(ctx, display, maxFPS)
	}
	backend, err := screencaptureBackend(preference)
	if err != nil {
		return nil, err
	}
	stream, err := screencapture.CaptureDisplay(ctx, display, screencapture.Options{
		Backend:    backend,
		FPS:        float64(maxFPS),
		QueueDepth: screencapture.MinQueueDepth,
		Timeout:    100 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	actual := protocol.DesktopCaptureGDI
	if stream.Backend() == screencapture.BackendDuplication {
		actual = protocol.DesktopCaptureDXGI
	}
	return &screencaptureFrameStream{stream: stream, backend: actual}, nil
}

func screencaptureBackend(preference protocol.DesktopCaptureBackend) (screencapture.Backend, error) {
	switch normalizedWindowsCaptureBackend(preference) {
	case protocol.DesktopCaptureAuto:
		return screencapture.BackendAuto, nil
	case protocol.DesktopCaptureDXGI:
		return screencapture.BackendDuplication, nil
	case protocol.DesktopCaptureGDI:
		return screencapture.BackendGDI, nil
	case protocol.DesktopCaptureWGC:
		return screencapture.BackendAuto, errWindowsGraphicsCaptureUnavailable
	default:
		return screencapture.BackendAuto, fmt.Errorf("unsupported Windows capture backend %q", preference)
	}
}

type screencaptureFrameStream struct {
	stream  *screencapture.Stream
	backend protocol.DesktopCaptureBackend
}

func (s *screencaptureFrameStream) Frame() (windowsCaptureFrame, bool) {
	if s == nil || s.stream == nil {
		return windowsCaptureFrame{}, false
	}
	frame, fresh := s.stream.Frame()
	return adaptScreencaptureFrame(frame), fresh
}

func (s *screencaptureFrameStream) WaitFrame(ctx context.Context) (windowsCaptureFrame, error) {
	if s == nil || s.stream == nil {
		return windowsCaptureFrame{}, screencapture.ErrBackendUnavailable
	}
	frame, err := s.stream.WaitFrame(ctx)
	if err != nil {
		return windowsCaptureFrame{}, err
	}
	return adaptScreencaptureFrame(frame), nil
}

func (s *screencaptureFrameStream) Backend() protocol.DesktopCaptureBackend {
	if s == nil {
		return ""
	}
	return s.backend
}

func (s *screencaptureFrameStream) Close() error {
	if s == nil || s.stream == nil {
		return nil
	}
	err := s.stream.Close()
	s.stream = nil
	return err
}

func adaptScreencaptureFrame(frame screencapture.Frame) windowsCaptureFrame {
	return windowsCaptureFrame{
		Pix:      frame.Pix,
		Width:    frame.Width,
		Height:   frame.Height,
		Stride:   frame.Stride,
		Sequence: frame.Seq,
		At:       frame.At,
	}
}
