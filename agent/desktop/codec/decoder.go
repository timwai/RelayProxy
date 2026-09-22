package codec

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrDecoderUnavailable = errors.New("video decoder unavailable")

type D3D11Surface struct {
	Resource    uintptr
	Subresource uint32

	releaseOnce sync.Once
	release     func()
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
