package codec

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type PixelFormat string

const (
	PixelFormatRGBA PixelFormat = "rgba"
	PixelFormatBGRA PixelFormat = "bgra"
	PixelFormatNV12 PixelFormat = "nv12"
	PixelFormatI444 PixelFormat = "i444"
)

type ChromaFormat string

const (
	Chroma420 ChromaFormat = "420"
	Chroma444 ChromaFormat = "444"
)

var (
	ErrInvalidVideoConfig        = errors.New("invalid video encoder configuration")
	ErrInvalidFrame              = errors.New("invalid video frame")
	ErrEncoderUnavailable        = errors.New("video encoder unavailable")
	ErrEncoderControlUnsupported = errors.New("video encoder control unsupported")
	ErrEncoderRebuildRequired    = errors.New("video encoder rebuild required")
)

type VideoConfig struct {
	Width             int
	Height            int
	FPS               int
	TargetBitrate     int
	KeyframeEvery     time.Duration
	Chroma            ChromaFormat
	BitDepth          int
	DisableLowLatency bool
}

func DefaultVideoConfig() VideoConfig {
	return VideoConfig{
		Width:         1280,
		Height:        720,
		FPS:           30,
		TargetBitrate: 6_000_000,
		KeyframeEvery: 2 * time.Second,
		Chroma:        Chroma420,
		BitDepth:      8,
	}
}

func NormalizeVideoConfig(cfg VideoConfig) (VideoConfig, error) {
	defaults := DefaultVideoConfig()
	if cfg.Width == 0 {
		cfg.Width = defaults.Width
	}
	if cfg.Height == 0 {
		cfg.Height = defaults.Height
	}
	if cfg.FPS == 0 {
		cfg.FPS = defaults.FPS
	}
	if cfg.TargetBitrate == 0 {
		cfg.TargetBitrate = defaults.TargetBitrate
	}
	if cfg.KeyframeEvery == 0 {
		cfg.KeyframeEvery = defaults.KeyframeEvery
	}
	if cfg.Chroma == "" {
		cfg.Chroma = defaults.Chroma
	}
	if cfg.BitDepth == 0 {
		cfg.BitDepth = defaults.BitDepth
	}

	if cfg.Width < 320 || cfg.Width > 3840 || cfg.Width%2 != 0 {
		return VideoConfig{}, fmt.Errorf("%w: width must be an even value between 320 and 3840", ErrInvalidVideoConfig)
	}
	if cfg.Height < 180 || cfg.Height > 2160 || cfg.Height%2 != 0 {
		return VideoConfig{}, fmt.Errorf("%w: height must be an even value between 180 and 2160", ErrInvalidVideoConfig)
	}
	if cfg.FPS < 1 || cfg.FPS > 60 {
		return VideoConfig{}, fmt.Errorf("%w: fps must be between 1 and 60", ErrInvalidVideoConfig)
	}
	if cfg.TargetBitrate < 250_000 || cfg.TargetBitrate > 100_000_000 {
		return VideoConfig{}, fmt.Errorf("%w: bitrate must be between 250 kbps and 100 Mbps", ErrInvalidVideoConfig)
	}
	if cfg.KeyframeEvery < 250*time.Millisecond || cfg.KeyframeEvery > 30*time.Second {
		return VideoConfig{}, fmt.Errorf("%w: keyframe interval must be between 250ms and 30s", ErrInvalidVideoConfig)
	}
	switch cfg.Chroma {
	case Chroma420, Chroma444:
	default:
		return VideoConfig{}, fmt.Errorf("%w: chroma must be 420 or 444", ErrInvalidVideoConfig)
	}
	if cfg.BitDepth != 8 {
		return VideoConfig{}, fmt.Errorf("%w: only 8-bit video is implemented in the current Relay Desktop pipeline", ErrInvalidVideoConfig)
	}
	return cfg, nil
}

func bitrateOnlyReconfigure(current, next VideoConfig) bool {
	return current.Width == next.Width &&
		current.Height == next.Height &&
		current.FPS == next.FPS &&
		current.KeyframeEvery == next.KeyframeEvery &&
		current.Chroma == next.Chroma &&
		current.BitDepth == next.BitDepth &&
		current.DisableLowLatency == next.DisableLowLatency
}

type RawFrame struct {
	Format    PixelFormat
	Pix       []byte
	Width     int
	Height    int
	Stride    int
	Timestamp time.Duration
}

func (f RawFrame) Validate() error {
	if f.Width <= 0 || f.Height <= 0 || f.Width%2 != 0 || f.Height%2 != 0 {
		return fmt.Errorf("%w: video frames require positive even dimensions", ErrInvalidFrame)
	}
	switch f.Format {
	case PixelFormatRGBA, PixelFormatBGRA:
		if f.Stride < f.Width*4 || len(f.Pix) < f.Stride*f.Height {
			return fmt.Errorf("%w: %s buffer is too small", ErrInvalidFrame, f.Format)
		}
	case PixelFormatNV12:
		if f.Stride < f.Width || len(f.Pix) < f.Stride*f.Height+f.Stride*(f.Height/2) {
			return fmt.Errorf("%w: NV12 buffer is too small", ErrInvalidFrame)
		}
	case PixelFormatI444:
		if f.Stride < f.Width || len(f.Pix) < f.Stride*f.Height*3 {
			return fmt.Errorf("%w: I444 buffer is too small", ErrInvalidFrame)
		}
	default:
		return fmt.Errorf("%w: unsupported pixel format %q", ErrInvalidFrame, f.Format)
	}
	return nil
}

type D3D11EncodeFrame struct {
	Resource    uintptr
	Subresource uint32
	Width       int
	Height      int
	Timestamp   time.Duration
}

func (f D3D11EncodeFrame) Validate() error {
	if f.Resource == 0 {
		return fmt.Errorf("%w: D3D11 resource is nil", ErrInvalidFrame)
	}
	if f.Width <= 0 || f.Height <= 0 || f.Width%2 != 0 || f.Height%2 != 0 {
		return fmt.Errorf("%w: D3D11 encode frames require positive even dimensions", ErrInvalidFrame)
	}
	return nil
}

type D3D11Encoder interface {
	EncodeD3D11(context.Context, D3D11EncodeFrame) ([]EncodedPacket, error)
}

type D3D11ConvertConfig struct {
	InputWidth   int
	InputHeight  int
	OutputWidth  int
	OutputHeight int
	FPS          int
}

func (c D3D11ConvertConfig) Validate() error {
	if c.InputWidth <= 0 || c.InputHeight <= 0 || c.OutputWidth <= 0 || c.OutputHeight <= 0 {
		return fmt.Errorf("%w: D3D11 converter dimensions must be positive", ErrInvalidVideoConfig)
	}
	if c.OutputWidth%2 != 0 || c.OutputHeight%2 != 0 {
		return fmt.Errorf("%w: D3D11 NV12 output requires even dimensions", ErrInvalidVideoConfig)
	}
	if c.FPS < 1 || c.FPS > 60 {
		return fmt.Errorf("%w: D3D11 converter fps must be between 1 and 60", ErrInvalidVideoConfig)
	}
	return nil
}

type EncodedPacket struct {
	Codec     string
	Data      []byte
	Timestamp time.Duration
	KeyFrame  bool
	Config    bool
}

type EncoderStats struct {
	Frames         uint64
	Bytes          uint64
	LastEncodeTime time.Duration
	Hardware       bool
	Backend        string
}

type Encoder interface {
	Encode(context.Context, RawFrame) ([]EncodedPacket, error)
	ForceIDR(context.Context) error
	Reconfigure(context.Context, VideoConfig) error
	Stats() EncoderStats
	Close() error
}
