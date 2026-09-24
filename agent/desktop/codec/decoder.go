package codec

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	desktopgpu "relayproxy/agent/desktop/gpu"
)

var ErrDecoderUnavailable = errors.New("video decoder unavailable")

type D3D11Surface struct {
	Device      uintptr
	Resource    uintptr
	Subresource uint32
	Format      PixelFormat

	releaseOnce sync.Once
	release     func()
	readback    func() ([]byte, error)
}

func (s *D3D11Surface) ReadNV12() ([]byte, error) {
	if s == nil || s.readback == nil || s.Format != PixelFormatNV12 {
		return nil, ErrDecoderUnavailable
	}
	return s.readback()
}

func (s *D3D11Surface) GPUFrame(width, height int) (desktopgpu.Frame, error) {
	if s == nil {
		return desktopgpu.Frame{}, ErrDecoderUnavailable
	}
	format, ok := gpuFormatForPixelFormat(s.Format)
	if !ok {
		return desktopgpu.Frame{}, fmt.Errorf("%w: unsupported D3D11 surface format %q", ErrDecoderUnavailable, s.Format)
	}
	frame := desktopgpu.Frame{
		Backend:     desktopgpu.BackendD3D11,
		Device:      s.Device,
		Resource:    s.Resource,
		Subresource: s.Subresource,
		Width:       width,
		Height:      height,
		Format:      format,
	}
	if err := frame.Validate(); err != nil {
		return desktopgpu.Frame{}, fmt.Errorf("%w: %v", ErrDecoderUnavailable, err)
	}
	return frame, nil
}

func gpuFormatForPixelFormat(format PixelFormat) (desktopgpu.Format, bool) {
	switch format {
	case PixelFormatNV12:
		return desktopgpu.FormatNV12, true
	case PixelFormatAYUV:
		return desktopgpu.FormatAYUV, true
	case PixelFormatP010:
		return desktopgpu.FormatP010, true
	case PixelFormatBGRA:
		return desktopgpu.FormatBGRA, true
	default:
		return "", false
	}
}

func (s *D3D11Surface) Close() {
	if s == nil {
		return
	}
	s.releaseOnce.Do(func() {
		if s.release != nil {
			s.release()
		}
	})
}

type DecodedFrame struct {
	Format    PixelFormat
	Pix       []byte
	Width     int
	Height    int
	Stride    int
	Timestamp time.Duration
	Hardware  bool
	D3D11     *D3D11Surface
}

func (f *DecodedFrame) Close() {
	if f == nil {
		return
	}
	if f.D3D11 != nil {
		f.D3D11.Close()
		f.D3D11 = nil
	}
}

type Decoder interface {
	Decode(context.Context, []byte, time.Duration) ([]DecodedFrame, error)
	Flush(context.Context) error
	Hardware() bool
	Backend() string
	Close() error
}
