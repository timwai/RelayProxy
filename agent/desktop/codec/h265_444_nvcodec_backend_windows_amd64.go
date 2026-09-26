//go:build windows && amd64

package codec

import "context"

func probeNVCodecH265444Backend(ctx context.Context) H265444BackendProbe {
	candidate := probeNVIDIAH265444RuntimeCandidate(ctx)
	return H265444BackendProbe{
		Backend:         H265444BackendNVCodec,
		HardwareRuntime: candidate.RuntimeAvailable && candidate.DeviceProbe,
		Encode:          candidate.EncodeCapabilityKnown && candidate.HEVC444Encode,
		Decode:          candidate.DecodeCapabilityKnown && candidate.HEVC444Decode,
		Error:           candidate.Error,
	}
}

// platformNVCodecH265444Backend is a canary-only production slot. It is placed
// ahead of oneVPL so an eligible explicit canary can try NVCodec first; when
// disabled or when an opener fails the generic registry continues to oneVPL.
func platformNVCodecH265444Backend() h265444Backend {
	return h265444Backend{
		name:              H265444BackendNVCodec,
		enabled:           NVCodecCanaryEnabled,
		probe:             probeNVCodecH265444Backend,
		openEncoderD3D11:  OpenNVENCH265EncoderWithD3D11,
		openDecoderD3D11:  OpenNVDECH265DecoderWithD3D11,
		productionReady:   true,
		zeroCopyValidated: true,
		lifecycle:         h265444SessionLifecycleContract(),
		interop:           h265444NVCodecAYUVContract(),
	}
}
