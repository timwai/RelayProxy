//go:build windows

package codec

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type MFH264Encoder struct {
	transform *MFH264Transform
	cfg       VideoConfig

	mu      sync.Mutex
	scratch []byte
	stats   EncoderStats
	closed  bool
}

func OpenMFH264Encoder(ctx context.Context, cfg VideoConfig, preferHardware bool) (*MFH264Encoder, error) {
	return openMFH264Encoder(ctx, cfg, preferHardware, 0)
}

func OpenMFH264EncoderWithD3D11(
	ctx context.Context,
	cfg VideoConfig,
	preferHardware bool,
	device uintptr,
) (*MFH264Encoder, error) {
	if device == 0 {
		return nil, ErrEncoderUnavailable
	}
	return openMFH264Encoder(ctx, cfg, preferHardware, device)
}

func openMFH264Encoder(
	ctx context.Context,
	cfg VideoConfig,
	preferHardware bool,
	device uintptr,
) (*MFH264Encoder, error) {
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Chroma != Chroma420 || cfg.BitDepth != 8 {
		return nil, fmt.Errorf("%w: Media Foundation H.264 only supports the Relay Desktop 8-bit 4:2:0 path", ErrEncoderUnavailable)
	}
	// Hardware Media Foundation encoders are commonly asynchronous MFTs.
	// The transform owns a blocking IMFMediaEventGenerator pump and therefore
	// does not poll for METransformNeedInput/METransformHaveOutput.
	var transform *MFH264Transform
	if device != 0 {
		transform, err = openMFH264TransformWithDevice(ctx, cfg, preferHardware, true, device)
	} else {
		transform, err = openMFH264Transform(ctx, cfg, preferHardware, true)
	}
	if err != nil {
		return nil, err
	}
	info := transform.Info()
	backend := "media-foundation"
	if info.D3D11Aware {
		backend = "media-foundation-d3d11"
	}
	return &MFH264Encoder{
		transform: transform,
		cfg:       cfg,
		stats: EncoderStats{
			Hardware: info.Hardware,
			Backend:  backend,
		},
	}, nil
}

func (e *MFH264Encoder) Encode(ctx context.Context, frame RawFrame) ([]EncodedPacket, error) {
	if e == nil {
		return nil, ErrEncoderUnavailable
	}
	start := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrEncoderUnavailable
	}
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return nil, ErrInvalidFrame
	}
	var err error
	e.scratch, err = frameToNV12(frame, e.scratch)
	if err != nil {
		return nil, err
	}
	packets, err := e.transform.EncodeNV12(ctx, e.scratch, frame.Timestamp)
	if err != nil {
		return nil, err
	}
	for _, packet := range packets {
		e.stats.Bytes += uint64(len(packet.Data))
	}
	e.stats.Frames++
	e.stats.LastEncodeTime = time.Since(start)
	return packets, nil
}

func (e *MFH264Encoder) EncodeD3D11(
	ctx context.Context,
	frame D3D11EncodeFrame,
) ([]EncodedPacket, error) {
	if e == nil {
		return nil, ErrEncoderUnavailable
	}
	start := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.transform == nil {
		return nil, ErrEncoderUnavailable
	}
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	if frame.Width != e.cfg.Width || frame.Height != e.cfg.Height {
		return nil, ErrInvalidFrame
	}
	packets, err := e.transform.EncodeD3D11(ctx, frame)
	if err != nil {
		return nil, err
	}
	for _, packet := range packets {
		e.stats.Bytes += uint64(len(packet.Data))
	}
	e.stats.Frames++
	e.stats.LastEncodeTime = time.Since(start)
	return packets, nil
}

func (e *MFH264Encoder) ForceIDR(ctx context.Context) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.transform == nil {
		return ErrEncoderUnavailable
	}
	return e.transform.ForceIDR(ctx)
}

func (e *MFH264Encoder) Reconfigure(ctx context.Context, cfg VideoConfig) error {
	if e == nil {
		return ErrEncoderUnavailable
	}
	cfg, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.transform == nil {
		return ErrEncoderUnavailable
	}
	if !bitrateOnlyReconfigure(e.cfg, cfg) {
		return ErrEncoderRebuildRequired
	}
	if cfg.TargetBitrate == e.cfg.TargetBitrate {
		return nil
	}
	if err := e.transform.SetBitrate(ctx, cfg.TargetBitrate); err != nil {
		return err
	}
	e.cfg.TargetBitrate = cfg.TargetBitrate
	return nil
}

func (e *MFH264Encoder) SequenceHeader() []byte {
	if e == nil || e.transform == nil {
		return nil
	}
	return append([]byte(nil), e.transform.Info().SequenceHeader...)
}

func (e *MFH264Encoder) Stats() EncoderStats {
	if e == nil {
		return EncoderStats{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

func (e *MFH264Encoder) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	transform := e.transform
	e.transform = nil
	e.mu.Unlock()
	if transform != nil {
		return transform.Close()
	}
	return nil
}
