//go:build windows && amd64

package codec

// platformNVCodecH265444Backend registers only the NVIDIA production slot, not
// a second runtime probe or production openers. Real NVENC/NVDEC device
// capability remains in ProbeH265444RuntimeCandidates so startup does not run
// the expensive vendor device probe twice.
//
// The slot must remain non-selectable until NVENC + NVDEC sessions implement
// the lifecycle contract and a real AYUV D3D11<->CUDA encode/decode round trip
// validates zero-copy on the same adapter.
func platformNVCodecH265444Backend() h265444Backend {
	return h265444Backend{
		name:              H265444BackendNVCodec,
		productionReady:   false,
		zeroCopyValidated: false,
		lifecycle:         h265444SessionLifecycleContract(),
		interop:           h265444NVCodecAYUVContract(),
	}
}
