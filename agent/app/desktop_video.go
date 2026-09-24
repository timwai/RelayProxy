package app

import (
	"fmt"
	"strings"

	desktopcodec "relayproxy/agent/desktop/codec"
	"relayproxy/internal/protocol"
)

func normalizeRemoteDesktopChroma(value protocol.DesktopChroma) (protocol.DesktopChroma, error) {
	switch protocol.DesktopChroma(strings.ToLower(strings.TrimSpace(string(value)))) {
	case "", protocol.DesktopChromaAuto:
		return protocol.DesktopChromaAuto, nil
	case protocol.DesktopChroma420:
		return protocol.DesktopChroma420, nil
	case protocol.DesktopChroma444:
		return protocol.DesktopChroma444, nil
	default:
		return "", fmt.Errorf("unsupported Relay Desktop chroma %q", value)
	}
}

func desktopCodecCapability(
	capabilities []protocol.DesktopCodecCapability,
	codec string,
) (protocol.DesktopCodecCapability, bool) {
	codec = desktopcodec.NormalizeCodecPreference(codec)
	if codec == "auto" || codec == "jpeg" {
		return protocol.DesktopCodecCapability{}, false
	}
	for _, capability := range capabilities {
		if desktopcodec.NormalizeCodecPreference(capability.Codec) == codec {
			return capability, true
		}
	}
	return protocol.DesktopCodecCapability{}, false
}

func negotiateRemoteDesktopVideo(
	target protocol.RemoteDesktopTarget,
	localCapabilities []protocol.DesktopCodecCapability,
	options protocol.RemoteDesktopConnectOptions,
) (protocol.RemoteDesktopConnectOptions, error) {
	chroma, err := normalizeRemoteDesktopChroma(options.Chroma)
	if err != nil {
		return options, err
	}
	options.Chroma = chroma

	raw := strings.TrimSpace(options.Codec)
	if strings.EqualFold(raw, protocol.DesktopCodecH265Validation) {
		// Preserve the diagnostics-only sentinel. It intentionally bypasses
		// public codec capability negotiation so validation can exercise fallback.
		return options, nil
	}

	preference := desktopcodec.NormalizeCodecPreference(raw)
	options.Codec = preference

	if preference == "h265" {
		targetCodec, ok := desktopCodecCapability(target.Capabilities.Codecs, "h265")
		if !ok || !targetCodec.Encode {
			return options, fmt.Errorf("Relay Desktop target does not advertise H.265 encode support")
		}
		localCodec, ok := desktopCodecCapability(localCapabilities, "h265")
		if !ok || !localCodec.Decode {
			return options, fmt.Errorf("this device does not advertise H.265 decode support")
		}
	}

	if chroma != protocol.DesktopChroma444 {
		return options, nil
	}
	if preference != "h264" && preference != "h265" {
		return options, fmt.Errorf("Relay Desktop 4:4:4 requires an explicit H.264 or H.265 codec")
	}
	targetCodec, ok := desktopCodecCapability(target.Capabilities.Codecs, preference)
	if !ok || !targetCodec.Encode || !targetCodec.Chroma444 {
		return options, fmt.Errorf("Relay Desktop target does not advertise %s 4:4:4 encode support", strings.ToUpper(preference))
	}
	localCodec, ok := desktopCodecCapability(localCapabilities, preference)
	if !ok || !localCodec.Decode || !localCodec.Chroma444 {
		return options, fmt.Errorf("this device does not advertise %s 4:4:4 decode support", strings.ToUpper(preference))
	}
	return options, nil
}
