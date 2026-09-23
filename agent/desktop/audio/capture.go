package audio

import (
	"context"
	"fmt"
	"time"
)

const DefaultLoopbackFrameDuration = 20 * time.Millisecond

type Capture interface {
	Read(context.Context) ([]byte, error)
	Close() error
}

func NormalizeFrameDuration(cfg PCMConfig, duration time.Duration) (PCMConfig, time.Duration, int, error) {
	cfg, err := NormalizePCMConfig(cfg)
	if err != nil {
		return PCMConfig{}, 0, 0, err
	}
	if duration <= 0 {
		duration = DefaultLoopbackFrameDuration
	}
	if duration > time.Second {
		return PCMConfig{}, 0, 0, fmt.Errorf("audio frame duration %s exceeds 1s", duration)
	}
	numerator := int64(cfg.SampleRate) * duration.Nanoseconds()
	if numerator%int64(time.Second) != 0 {
		return PCMConfig{}, 0, 0, fmt.Errorf("audio frame duration %s does not contain a whole number of samples at %d Hz", duration, cfg.SampleRate)
	}
	frames := numerator / int64(time.Second)
	if frames <= 0 {
		return PCMConfig{}, 0, 0, fmt.Errorf("audio frame duration %s is too small", duration)
	}
	bytes := frames * int64(cfg.BlockAlign())
	if bytes <= 0 || bytes > 16<<20 {
		return PCMConfig{}, 0, 0, fmt.Errorf("audio frame payload size %d is invalid", bytes)
	}
	return cfg, duration, int(bytes), nil
}
