//go:build windows && amd64

package codec

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"unsafe"
)

const (
	nvencConfigSize           = 3584
	nvencPresetConfigSize     = 5128
	nvencInitializeParamsSize = 1808

	nvencPresetConfigConfigOffset = 8

	nvencConfigVersionOffset          = 0
	nvencConfigProfileGUIDOffset      = 4
	nvencConfigGOPLengthOffset        = 20
	nvencConfigFrameIntervalPOffset   = 24
	nvencConfigRCParamsOffset         = 40
	nvencConfigCodecConfigOffset      = 168
	nvencConfigHEVCBitfieldOffset     = nvencConfigCodecConfigOffset + 16
	nvencConfigHEVCIDRPeriodOffset    = nvencConfigCodecConfigOffset + 20
	nvencConfigRCRateControlOffset    = nvencConfigRCParamsOffset + 4
	nvencConfigRCAverageBitrateOffset = nvencConfigRCParamsOffset + 20
	nvencConfigRCMaxBitrateOffset     = nvencConfigRCParamsOffset + 24
	nvencConfigRCVBVSizeOffset        = nvencConfigRCParamsOffset + 28
	nvencConfigRCVBVDelayOffset       = nvencConfigRCParamsOffset + 32
	nvencConfigRCFlagsOffset          = nvencConfigRCParamsOffset + 36
	nvencConfigRCLookaheadDepthOffset = nvencConfigRCParamsOffset + 90

	nvencInitializeVersionOffset      = 0
	nvencInitializeEncodeGUIDOffset   = 4
	nvencInitializePresetGUIDOffset   = 20
	nvencInitializeWidthOffset        = 36
	nvencInitializeHeightOffset       = 40
	nvencInitializeDARWidthOffset     = 44
	nvencInitializeDARHeightOffset    = 48
	nvencInitializeFrameRateNumOffset = 52
	nvencInitializeFrameRateDenOffset = 56
	nvencInitializeEnableAsyncOffset  = 60
	nvencInitializeEnablePTDOffset    = 64
	nvencInitializeEncodeConfigOffset = 88
	nvencInitializeMaxWidthOffset     = 96
	nvencInitializeMaxHeightOffset    = 100
	nvencInitializeTuningInfoOffset   = 136
	nvencInitializeBufferFormatOffset = 140

	nvencTuningHighQuality     uint32 = 1
	nvencTuningUltraLowLatency uint32 = 3
	nvencRateControlCBR        uint32 = 2

	nvencRCFlagEnableLookahead uint32 = 1 << 5
	nvencRCFlagZeroReorder     uint32 = 1 << 9

	nvencHEVCRepeatSPSPPS uint32 = 1 << 7
	nvencHEVCChromaMask   uint32 = 3 << 9
	nvencHEVCChroma444    uint32 = 3 << 9
)

var (
	nvencPresetLowLatencyHQGUID = nvencGUID{
		Data1: 0xc5f733b9,
		Data2: 0xea97,
		Data3: 0x4cf9,
		Data4: [8]byte{0xbe, 0xc2, 0xbf, 0x78, 0xa7, 0x4f, 0xd1, 0x05},
	}
	nvencPresetP1GUID = nvencGUID{
		Data1: 0xfc0a8d3e,
		Data2: 0x45f8,
		Data3: 0x4cf8,
		Data4: [8]byte{0x80, 0xc7, 0x29, 0x88, 0x71, 0x59, 0x0e, 0xbf},
	}
	nvencHEVCFRExtGUID = nvencGUID{
		Data1: 0x51ec32b5,
		Data2: 0x1b4c,
		Data3: 0x453c,
		Data4: [8]byte{0x9c, 0xbd, 0xb6, 0x16, 0xbd, 0x62, 0x13, 0x41},
	}
)

type nvencConfigBlob struct {
	_    [0]uintptr
	Data [nvencConfigSize]byte
}

type nvencPresetConfigBlob struct {
	_    [0]uintptr
	Data [nvencPresetConfigSize]byte
}

type nvencInitializeParamsBlob struct {
	_    [0]uintptr
	Data [nvencInitializeParamsSize]byte
}

func nvencVersionWithReservedBitFor(apiVersion, structVersion uint32) uint32 {
	return nvencStructVersionFor(apiVersion, structVersion) | 1<<31
}

func nvencVersionWithReservedBit(structVersion uint32) uint32 {
	return nvencVersionWithReservedBitFor(nvencAPIVersion, structVersion)
}

func putNVENCGUID(dst []byte, offset int, guid nvencGUID) {
	binary.LittleEndian.PutUint32(dst[offset:offset+4], guid.Data1)
	binary.LittleEndian.PutUint16(dst[offset+4:offset+6], guid.Data2)
	binary.LittleEndian.PutUint16(dst[offset+6:offset+8], guid.Data3)
	copy(dst[offset+8:offset+16], guid.Data4[:])
}

func nvencGOPFrames(cfg VideoConfig) uint32 {
	frames := int(math.Round(cfg.KeyframeEvery.Seconds() * float64(cfg.FPS)))
	if frames < 1 {
		frames = 1
	}
	return uint32(frames)
}

func nvencTuningForConfig(cfg VideoConfig) uint32 {
	if cfg.DisableLowLatency {
		return nvencTuningHighQuality
	}
	return nvencTuningUltraLowLatency
}

func prepareNVENCPresetConfig(apiVersion uint32, legacy bool) *nvencPresetConfigBlob {
	preset := &nvencPresetConfigBlob{}
	presetVersion := uint32(5)
	configVersion := uint32(9)
	if legacy {
		presetVersion = 4
		configVersion = 7
	}
	binary.LittleEndian.PutUint32(
		preset.Data[0:4],
		nvencVersionWithReservedBitFor(apiVersion, presetVersion),
	)
	binary.LittleEndian.PutUint32(
		preset.Data[nvencPresetConfigConfigOffset:nvencPresetConfigConfigOffset+4],
		nvencVersionWithReservedBitFor(apiVersion, configVersion),
	)
	return preset
}

func configFromNVENCPreset(preset *nvencPresetConfigBlob) *nvencConfigBlob {
	cfg := &nvencConfigBlob{}
	if preset == nil {
		return cfg
	}
	copy(
		cfg.Data[:],
		preset.Data[nvencPresetConfigConfigOffset:nvencPresetConfigConfigOffset+nvencConfigSize],
	)
	return cfg
}

func configureNVENCHEVC444(config *nvencConfigBlob, cfg VideoConfig, apiVersion uint32, legacy bool) error {
	if config == nil {
		return ErrEncoderUnavailable
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return fmt.Errorf("%w: NVENC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}
	cfg = normalized

	configVersion := uint32(9)
	if legacy {
		configVersion = 7
	}
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigVersionOffset:nvencConfigVersionOffset+4],
		nvencVersionWithReservedBitFor(apiVersion, configVersion),
	)
	putNVENCGUID(config.Data[:], nvencConfigProfileGUIDOffset, nvencHEVCFRExtGUID)

	gop := nvencGOPFrames(cfg)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigGOPLengthOffset:nvencConfigGOPLengthOffset+4],
		gop,
	)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigFrameIntervalPOffset:nvencConfigFrameIntervalPOffset+4],
		1,
	)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigHEVCIDRPeriodOffset:nvencConfigHEVCIDRPeriodOffset+4],
		gop,
	)

	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigRCParamsOffset:nvencConfigRCParamsOffset+4],
		nvencStructVersionFor(apiVersion, 1),
	)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigRCRateControlOffset:nvencConfigRCRateControlOffset+4],
		nvencRateControlCBR,
	)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigRCAverageBitrateOffset:nvencConfigRCAverageBitrateOffset+4],
		uint32(cfg.TargetBitrate),
	)
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigRCMaxBitrateOffset:nvencConfigRCMaxBitrateOffset+4],
		uint32(cfg.TargetBitrate),
	)
	binary.LittleEndian.PutUint16(
		config.Data[nvencConfigRCLookaheadDepthOffset:nvencConfigRCLookaheadDepthOffset+2],
		0,
	)

	rcFlags := binary.LittleEndian.Uint32(
		config.Data[nvencConfigRCFlagsOffset : nvencConfigRCFlagsOffset+4],
	)
	rcFlags &^= nvencRCFlagEnableLookahead
	if cfg.DisableLowLatency {
		rcFlags &^= nvencRCFlagZeroReorder
		binary.LittleEndian.PutUint32(
			config.Data[nvencConfigRCVBVSizeOffset:nvencConfigRCVBVSizeOffset+4],
			0,
		)
		binary.LittleEndian.PutUint32(
			config.Data[nvencConfigRCVBVDelayOffset:nvencConfigRCVBVDelayOffset+4],
			0,
		)
	} else {
		rcFlags |= nvencRCFlagZeroReorder
		vbv := cfg.TargetBitrate / cfg.FPS
		if vbv < 1 {
			vbv = 1
		}
		binary.LittleEndian.PutUint32(
			config.Data[nvencConfigRCVBVSizeOffset:nvencConfigRCVBVSizeOffset+4],
			uint32(vbv),
		)
		binary.LittleEndian.PutUint32(
			config.Data[nvencConfigRCVBVDelayOffset:nvencConfigRCVBVDelayOffset+4],
			uint32(vbv),
		)
	}
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigRCFlagsOffset:nvencConfigRCFlagsOffset+4],
		rcFlags,
	)

	hevcFlags := binary.LittleEndian.Uint32(
		config.Data[nvencConfigHEVCBitfieldOffset : nvencConfigHEVCBitfieldOffset+4],
	)
	hevcFlags &^= nvencHEVCChromaMask
	hevcFlags |= nvencHEVCChroma444 | nvencHEVCRepeatSPSPPS
	binary.LittleEndian.PutUint32(
		config.Data[nvencConfigHEVCBitfieldOffset:nvencConfigHEVCBitfieldOffset+4],
		hevcFlags,
	)
	return nil
}

func buildNVENCInitializeParams(
	config *nvencConfigBlob,
	cfg VideoConfig,
	apiVersion uint32,
	legacy bool,
) (*nvencInitializeParamsBlob, error) {
	if config == nil {
		return nil, ErrEncoderUnavailable
	}
	normalized, err := NormalizeVideoConfig(cfg)
	if err != nil {
		return nil, err
	}
	if normalized.Chroma != Chroma444 || normalized.BitDepth != 8 {
		return nil, fmt.Errorf("%w: NVENC HEVC requires 8-bit 4:4:4 video", ErrInvalidVideoConfig)
	}
	cfg = normalized

	params := &nvencInitializeParamsBlob{}
	initVersion := uint32(7)
	presetGUID := nvencPresetP1GUID
	if legacy {
		initVersion = 5
		presetGUID = nvencPresetLowLatencyHQGUID
	}
	binary.LittleEndian.PutUint32(
		params.Data[nvencInitializeVersionOffset:nvencInitializeVersionOffset+4],
		nvencVersionWithReservedBitFor(apiVersion, initVersion),
	)
	putNVENCGUID(params.Data[:], nvencInitializeEncodeGUIDOffset, nvencCodecHEVCGUID)
	putNVENCGUID(params.Data[:], nvencInitializePresetGUIDOffset, presetGUID)
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeWidthOffset:nvencInitializeWidthOffset+4], uint32(cfg.Width))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeHeightOffset:nvencInitializeHeightOffset+4], uint32(cfg.Height))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeDARWidthOffset:nvencInitializeDARWidthOffset+4], uint32(cfg.Width))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeDARHeightOffset:nvencInitializeDARHeightOffset+4], uint32(cfg.Height))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeFrameRateNumOffset:nvencInitializeFrameRateNumOffset+4], uint32(cfg.FPS))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeFrameRateDenOffset:nvencInitializeFrameRateDenOffset+4], 1)
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeEnableAsyncOffset:nvencInitializeEnableAsyncOffset+4], 0)
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeEnablePTDOffset:nvencInitializeEnablePTDOffset+4], 1)
	binary.LittleEndian.PutUint64(
		params.Data[nvencInitializeEncodeConfigOffset:nvencInitializeEncodeConfigOffset+8],
		uint64(uintptr(unsafe.Pointer(&config.Data[0]))),
	)
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeMaxWidthOffset:nvencInitializeMaxWidthOffset+4], uint32(cfg.Width))
	binary.LittleEndian.PutUint32(params.Data[nvencInitializeMaxHeightOffset:nvencInitializeMaxHeightOffset+4], uint32(cfg.Height))
	if !legacy {
		binary.LittleEndian.PutUint32(params.Data[nvencInitializeTuningInfoOffset:nvencInitializeTuningInfoOffset+4], nvencTuningForConfig(cfg))
		binary.LittleEndian.PutUint32(params.Data[nvencInitializeBufferFormatOffset:nvencInitializeBufferFormatOffset+4], uint32(nvencBufferFormatAYUV))
	}
	return params, nil
}

func (s *nvencD3D11Session) initializeHEVC444(
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
	if s.closed || s.encoder == 0 {
		return VideoConfig{}, ErrEncoderUnavailable
	}
	if s.initialized {
		if s.videoConfig == normalized {
			return normalized, nil
		}
		return VideoConfig{}, ErrEncoderRebuildRequired
	}
	preset := prepareNVENCPresetConfig(s.apiVersion, s.legacyABI)
	hevcGUID := nvencCodecHEVCGUID
	presetGUID := nvencPresetP1GUID
	var status int32
	if s.legacyABI {
		if s.api.NvEncGetEncodePresetConfig == 0 {
			return VideoConfig{}, fmt.Errorf("%w: nvEncGetEncodePresetConfig is unavailable", ErrEncoderUnavailable)
		}
		presetGUID = nvencPresetLowLatencyHQGUID
		status = nvencCall(
			s.api.NvEncGetEncodePresetConfig,
			s.encoder,
			uintptr(unsafe.Pointer(&hevcGUID)),
			uintptr(unsafe.Pointer(&presetGUID)),
			uintptr(unsafe.Pointer(&preset.Data[0])),
		)
	} else {
		if s.api.NvEncGetEncodePresetConfigEx == 0 {
			return VideoConfig{}, fmt.Errorf("%w: nvEncGetEncodePresetConfigEx is unavailable", ErrEncoderUnavailable)
		}
		status = nvencCall(
			s.api.NvEncGetEncodePresetConfigEx,
			s.encoder,
			uintptr(unsafe.Pointer(&hevcGUID)),
			uintptr(unsafe.Pointer(&presetGUID)),
			uintptr(nvencTuningForConfig(normalized)),
			uintptr(unsafe.Pointer(&preset.Data[0])),
		)
	}
	runtime.KeepAlive(&hevcGUID)
	runtime.KeepAlive(&presetGUID)
	runtime.KeepAlive(preset)
	if status != 0 {
		return VideoConfig{}, fmt.Errorf("%w: NVENC preset config query returned %d", ErrEncoderUnavailable, status)
	}

	config := configFromNVENCPreset(preset)
	if err := configureNVENCHEVC444(config, normalized, s.apiVersion, s.legacyABI); err != nil {
		return VideoConfig{}, err
	}
	params, err := buildNVENCInitializeParams(config, normalized, s.apiVersion, s.legacyABI)
	if err != nil {
		return VideoConfig{}, err
	}
	status = nvencCall(
		s.api.NvEncInitializeEncoder,
		s.encoder,
		uintptr(unsafe.Pointer(&params.Data[0])),
	)
	runtime.KeepAlive(config)
	runtime.KeepAlive(params)
	if status != 0 {
		return VideoConfig{}, fmt.Errorf("%w: nvEncInitializeEncoder returned %d", ErrEncoderUnavailable, status)
	}

	s.initialized = true
	s.videoConfig = normalized
	s.initConfig = config
	s.initParams = params
	return normalized, nil
}
