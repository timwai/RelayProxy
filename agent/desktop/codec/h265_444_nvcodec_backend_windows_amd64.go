//go:build windows && amd64

package codec

import "context"

func probeNVCodecH265444Backend(ctx context.Context) H265444BackendProbe {
	candidate := probeNVIDIAH265444RuntimeCandidate(ctx)
	return H265444BackendProbe{
		Backend:         H265444BackendNVCodec,
		HardwareRuntime: candidate.RuntimeAvailable,
		Encode:          candidate.EncodeCapabilityKnown && candidate.HEVC444Encode,
		Decode:          candidate.DecodeCapabilityKnown && candidate.HEVC444Decode,
		Error:           candidate.Error,
	}
}

// platformNVCodecH265444Backend intentionally registers only the NVIDIA
// production boundary, not production openers. Runtime/device capability is
// useful diagnostics, but it must remain non-selectable until NVENC + NVDEC
// sessions implement the lifecycle contract and a real AYUV D3D11<->CUDA
// encode/decode round trip validates zero-copy on the same adapter.
func platformNVCodecH265444Backend() h265444Backend {
	return h265444Backend{
		name:              H265444BackendNVCodec,
		probe:             probeNVCodecH265444Backend,
		productionReady:   false,
		zeroCopyValidated: false,
		lifecycle:         h265444SessionLifecycleContract(),
		interop:           h265444D3D11CUDAAYUVContract(),
	}
}
