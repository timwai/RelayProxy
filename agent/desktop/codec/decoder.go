package codec

import (
	"context"
	"errors"
	"time"
)

var ErrDecoderUnavailable = errors.New("video decoder unavailable")

type DecodedFrame struct {
	Format    PixelFormat
	Pix       []byte
	Width     int
	Height    int
	Stride    int
	Timestamp time.Duration
	Hardware  bool
}

type Decoder interface {
	Decode(context.Context, []byte, time.Duration) ([]DecodedFrame, error)
	Flush(context.Context) error
	Hardware() bool
	Close() error
}
