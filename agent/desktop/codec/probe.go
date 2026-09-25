package codec

import (
	"strings"

	"relayproxy/internal/protocol"
)

// H264Probe summarizes the H.264 transforms Media Foundation can expose on the
// current machine. Counts are retained because some systems register multiple
// GPU-specific transforms and the encoder-selection stage will need to rank
// them rather than treat capability as a single boolean.
type H264Probe struct {
	MediaFoundation      bool
	HardwareEncoderCount int
	HardwareDecoderCount int
	SoftwareEncoderCount int
	SoftwareDecoderCount int
	Error                string
}

func (p H264Probe) EncodeAvailable() bool {
	return p.HardwareEncoderCount > 0 || p.SoftwareEncoderCount > 0
}

func (p H264Probe) DecodeAvailable() bool {
	return p.HardwareDecoderCount > 0 || p.SoftwareDecoderCount > 0
}

func (p H264Probe) HardwareEndToEnd() bool {
	return p.HardwareEncoderCount > 0 && p.HardwareDecoderCount > 0
}

func (p H264Probe) Capability() protocol.DesktopCodecCapability {
	capability := protocol.DesktopCodecCapability{
		Codec:     "h264",
		Encoder:   "media-foundation",
		Hardware:  p.HardwareEndToEnd(),
		Encode:    p.EncodeAvailable(),
		Decode:    p.DecodeAvailable(),
		Chroma420: p.EncodeAvailable() || p.DecodeAvailable(),
		BitDepth8: p.EncodeAvailable() || p.DecodeAvailable(),
	}
	if capability.Encode {
		capability.EncodeChroma = []string{"420"}
	} else {
		capability.Encoder = ""
	}
	if capability.Decode {
		capability.DecodeChroma = []string{"420"}
	}
	return capability
}

// H265Probe summarizes HEVC/H.265 transforms exposed by Media Foundation.
// Public Relay Desktop negotiation advertises this capability only when the
// current machine exposes at least an HEVC encoder or decoder.
type H265Probe struct {
	MediaFoundation      bool
	HardwareEncoderCount int
	HardwareDecoderCount int
	SoftwareEncoderCount int
	SoftwareDecoderCount int
	Error                string
}

func (p H265Probe) EncodeAvailable() bool {
	return p.HardwareEncoderCount > 0 || p.SoftwareEncoderCount > 0
}

func (p H265Probe) DecodeAvailable() bool {
	return p.HardwareDecoderCount > 0 || p.SoftwareDecoderCount > 0
}

func (p H265Probe) HardwareEndToEnd() bool {
	return p.HardwareEncoderCount > 0 && p.HardwareDecoderCount > 0
}

func (p H265Probe) Capability() protocol.DesktopCodecCapability {
	available := p.EncodeAvailable() || p.DecodeAvailable()
	capability := protocol.DesktopCodecCapability{
		Codec:     "h265",
		Encoder:   "media-foundation",
		Hardware:  p.HardwareEndToEnd(),
		Encode:    p.EncodeAvailable(),
		Decode:    p.DecodeAvailable(),
		Chroma420: available,
		BitDepth8: available,
	}
	if capability.Encode {
		capability.EncodeChroma = []string{"420"}
	} else {
		capability.Encoder = ""
	}
	if capability.Decode {
		capability.DecodeChroma = []string{"420"}
	}
	return capability
}

func appendDesktopChroma(values []string, chroma string) []string {
	for _, value := range values {
		if value == chroma {
			return values
		}
	}
	return append(values, chroma)
}

// H265CapabilityWith444Paths combines the established Media Foundation 4:2:0
// path with vendor-neutral HEVC 4:4:4 backends. System-memory backends are safe
// to aggregate directly because their openers can serve the generic H.265
// pipeline. A D3D11-only backend becomes public only after the Host has
// validated AYUV encode + decode + display on a real device.
func H265CapabilityWith444Paths(
	mf H265Probe,
	backends []H265444BackendProbe,
	d3d11EndToEndValidated bool,
) (protocol.DesktopCodecCapability, bool) {
	capability := mf.Capability()
	available := mf.EncodeAvailable() || mf.DecodeAvailable()

	systemMemory444 := H265444SystemMemoryEndToEndAvailable(backends)
	d3d11444 := d3d11EndToEndValidated && H265444D3D11EndToEndAvailable(backends)
	if systemMemory444 || d3d11444 {
		capability.Codec = "h265"
		capability.Encode = true
		capability.Decode = true
		// Legacy peers only understand the shared Chroma420 flag. Once a
		// 4:4:4 backend makes Encode/Decode both true, keep that legacy flag
		// conservative unless Media Foundation also supports both directions.
		capability.Chroma420 = mf.EncodeAvailable() && mf.DecodeAvailable()
		capability.Chroma444 = true
		capability.EncodeChroma = appendDesktopChroma(capability.EncodeChroma, "444")
		capability.DecodeChroma = appendDesktopChroma(capability.DecodeChroma, "444")
		capability.BitDepth8 = true
		capability.Hardware = true

		encoderBackends := make([]string, 0, len(backends))
		for _, backend := range backends {
			if !backend.HardwareRuntime || strings.TrimSpace(backend.Backend) == "" {
				continue
			}
			contributesSystemMemory := systemMemory444 && backend.SystemMemoryEncode
			contributesD3D11 := d3d11444 && backend.D3D11Encode
			if !contributesSystemMemory && !contributesD3D11 {
				continue
			}
			encoderBackends = append(encoderBackends, strings.TrimSpace(backend.Backend))
		}
		if len(encoderBackends) > 0 {
			label := strings.Join(encoderBackends, "+")
			if capability.Encoder == "" {
				capability.Encoder = label
			} else {
				capability.Encoder += "+" + label
			}
		}
		available = true
	}
	return capability, available
}

// H265CapabilityWith444Backends preserves the existing generic aggregation API.
// It intentionally treats D3D11-only paths as unvalidated; platform Hosts must
// call H265CapabilityWith444Paths after their device-level AYUV validation.
func H265CapabilityWith444Backends(
	mf H265Probe,
	backends []H265444BackendProbe,
) (protocol.DesktopCodecCapability, bool) {
	return H265CapabilityWith444Paths(mf, backends, false)
}

// H265Capability preserves the existing oneVPL-facing API while routing
// capability aggregation through the vendor-neutral backend model.
func H265Capability(mf H265Probe, oneVPL OneVPLProbe) (protocol.DesktopCodecCapability, bool) {
	return H265CapabilityWith444Backends(mf, []H265444BackendProbe{{
		Backend:            H265444BackendOneVPL,
		HardwareRuntime:    oneVPL.DispatcherAvailable && oneVPL.HardwareRuntime,
		Encode:             oneVPL.HEVC444Encode,
		Decode:             oneVPL.HEVC444Decode,
		SystemMemoryEncode: oneVPL.HEVC444Encode,
		SystemMemoryDecode: oneVPL.HEVC444Decode,
		D3D11Encode:        oneVPL.HEVC444Encode,
		D3D11Decode:        oneVPL.HEVC444Decode,
		Error:              oneVPL.Error,
	}})
}
