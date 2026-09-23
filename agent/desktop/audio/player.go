package audio

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrUnavailable = errors.New("Relay Desktop audio playback is unavailable")
	ErrClosed      = errors.New("Relay Desktop audio playback is closed")
)

// PCMConfig is the first Relay Desktop audio transport format. The initial
// native bring-up is deliberately fixed to signed 16-bit little-endian PCM;
// compressed codecs can reuse the session generation model later.
type PCMConfig struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
}

func NormalizePCMConfig(cfg PCMConfig) (PCMConfig, error) {
	if cfg.SampleRate < 8_000 || cfg.SampleRate > 192_000 {
		return PCMConfig{}, fmt.Errorf("invalid PCM sample rate %d", cfg.SampleRate)
	}
	if cfg.Channels < 1 || cfg.Channels > 8 {
		return PCMConfig{}, fmt.Errorf("invalid PCM channel count %d", cfg.Channels)
	}
	if cfg.BitsPerSample == 0 {
		cfg.BitsPerSample = 16
	}
	if cfg.BitsPerSample != 16 {
		return PCMConfig{}, fmt.Errorf("unsupported PCM bit depth %d", cfg.BitsPerSample)
	}
	return cfg, nil
}

func (c PCMConfig) BlockAlign() int {
	if c.Channels <= 0 || c.BitsPerSample <= 0 || c.BitsPerSample%8 != 0 {
		return 0
	}
	return c.Channels * (c.BitsPerSample / 8)
}

func (c PCMConfig) BytesPerSecond() int {
	return c.SampleRate * c.BlockAlign()
}

func (c PCMConfig) ValidatePayload(data []byte) error {
	blockAlign := c.BlockAlign()
	if blockAlign <= 0 {
		return errors.New("invalid PCM block alignment")
	}
	if len(data)%blockAlign != 0 {
		return fmt.Errorf("PCM payload size %d is not aligned to %d bytes", len(data), blockAlign)
	}
	return nil
}

type Player interface {
	Write(context.Context, []byte) error
	Close() error
}
