package codec

import "relayproxy/internal/protocol"

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
		Chroma420: true,
		BitDepth8: true,
	}
	if !capability.Encode {
		capability.Encoder = ""
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
	if !capability.Encode {
		capability.Encoder = ""
	}
	return capability
}

// H265Capability combines the established Media Foundation 4:2:0 path with
// the oneVPL HEVC RExt 4:4:4 path. Because DesktopCodecCapability currently
// has shared chroma flags rather than direction-specific chroma flags, 4:4:4
// is advertised only when this machine can both encode and decode it.
func H265Capability(mf H265Probe, oneVPL OneVPLProbe) (protocol.DesktopCodecCapability, bool) {
	capability := mf.Capability()
	available := mf.EncodeAvailable() || mf.DecodeAvailable()

	if oneVPL.HEVC444EndToEnd() {
		capability.Codec = "h265"
		capability.Encode = true
		capability.Decode = true
		capability.Chroma444 = true
		capability.BitDepth8 = true
		capability.Hardware = true
		if capability.Encoder == "" {
			capability.Encoder = "onevpl-hevc444"
		} else {
			capability.Encoder = "media-foundation+onevpl-hevc444"
		}
		available = true
	}
	return capability, available
}
