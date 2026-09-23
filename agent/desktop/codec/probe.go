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
// It is intentionally separate from the advertised Host codec list until the
// full H.265 encode/decode session path is implemented.
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
	capability := protocol.DesktopCodecCapability{
		Codec:     "h265",
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
