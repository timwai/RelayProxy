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
	PixelFormatNV12 PixelFormat = "nv12"
)

var (
	ErrInvalidVideoConfig = errors.New("invalid video encoder configuration")
	ErrInvalidFrame       = errors.New("invalid video frame")
	ErrEncoderUnavailable = errors.New("video encoder unavailable")
)

type VideoConfig struct {
	Width         int
	Height        int
	FPS           int
	TargetBitrate int
	KeyframeEvery time.Duration
	LowLatency    bool
}

func DefaultVideoConfig() VideoConfig {
	return VideoConfig{
		Width:         1280,
		Height:        720,
		FPS:           30,
		TargetBitrate: 6_000_000,
		KeyframeEvery: 2 * time.Second,
		LowLatency:    true,
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
	return cfg, nil
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
		return fmt.Errorf("%w: H.264 4:2:0 frames require positive even dimensions", ErrInvalidFrame)
	}
	switch f.Format {
	case PixelFormatRGBA:
		if f.Stride < f.Width*4 || len(f.Pix) < f.Stride*f.Height {
			return fmt.Errorf("%w: RGBA buffer is too small", ErrInvalidFrame)
		}
	case PixelFormatNV12:
		if f.Stride < f.Width || len(f.Pix) < f.Stride*f.Height+f.Stride*(f.Height/2) {
			return fmt.Errorf("%w: NV12 buffer is too small", ErrInvalidFrame)
		}
	default:
		return fmt.Errorf("%w: unsupported pixel format %q", ErrInvalidFrame, f.Format)
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
