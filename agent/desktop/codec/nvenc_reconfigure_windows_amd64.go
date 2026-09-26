//go:build windows && amd64

package codec

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"
)

const (
	nvencReconfigureParamsSize = 1824

	nvencReconfigureVersionOffset = 0
	nvencReconfigureInitOffset    = 8
	nvencReconfigureFlagsOffset   = 1816

	nvencReconfigureForceIDR uint32 = 1 << 1
)

type nvencReconfigureParamsBlob struct {
	_    [0]uintptr
	Data [nvencReconfigureParamsSize]byte
}

func buildNVENCReconfigureParams(
	config *nvencConfigBlob,
	cfg VideoConfig,
	forceIDR bool,
) (*nvencInitializeParamsBlob, *nvencReconfigureParamsBlob, error) {
	initParams, err := buildNVENCInitializeParams(config, cfg)
	if err != nil {
		return nil, nil, err
	}

	params := &nvencReconfigureParamsBlob{}
	binary.LittleEndian.PutUint32(
		params.Data[nvencReconfigureVersionOffset:nvencReconfigureVersionOffset+4],
		nvencVersionWithReservedBit(2),
	)
	copy(
		params.Data[nvencReconfigureInitOffset:nvencReconfigureInitOffset+nvencInitializeParamsSize],
		initParams.Data[:],
	)
	if forceIDR {
		binary.LittleEndian.PutUint32(
			params.Data[nvencReconfigureFlagsOffset:nvencReconfigureFlagsOffset+4],
			nvencReconfigureForceIDR,
		)
	}
	return initParams, params, nil
}

func (s *nvencD3D11Session) reconfigureHEVC444Bitrate(
	ctx context.Context,
	cfg VideoConfig,
) (VideoConfig, error) {
	if s == nil {
		return VideoConfig{}, ErrEncoderUnavailable
	}
	if err := ctx.Err(); err != nil {
		return VideoConfig{}, err
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return VideoConfig{}, err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return VideoConfig{}, fmt.Errorf("%w: NVENC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.encoder == 0 || !s.initialized || s.initConfig == nil {
		return VideoConfig{}, ErrEncoderUnavailable
	}
	if !bitrateOnlyReconfigure(s.videoConfig, normalized) {
		return VideoConfig{}, ErrEncoderRebuildRequired
	}
	if normalized.TargetBitrate == s.videoConfig.TargetBitrate {
		return normalized, nil
	}
	if s.api.NvEncReconfigureEncoder == 0 {
		return VideoConfig{}, fmt.Errorf("%w: nvEncReconfigureEncoder is unavailable", ErrEncoderControlUnsupported)
	}

	config := &nvencConfigBlob{}
	copy(config.Data[:], s.initConfig.Data[:])
	if err := configureNVENCHEVC444(config, normalized); err != nil {
		return VideoConfig{}, err
	}
	initParams, reconfigureParams, err := buildNVENCReconfigureParams(config, normalized, false)
	if err != nil {
		return VideoConfig{}, err
	}

	status := nvencCall(
		s.api.NvEncReconfigureEncoder,
		s.encoder,
		uintptr(unsafe.Pointer(&reconfigureParams.Data[0])),
	)
	runtime.KeepAlive(config)
	runtime.KeepAlive(initParams)
	runtime.KeepAlive(reconfigureParams)
	if status != 0 {
		return VideoConfig{}, fmt.Errorf("nvEncReconfigureEncoder returned %d", status)
	}

	s.videoConfig = normalized
	s.initConfig = config
	s.initParams = initParams
	return normalized, nil
}
