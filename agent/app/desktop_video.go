package app

import (
	"fmt"
	"strings"

	desktopcodec "relayproxy/agent/desktop/codec"
	"relayproxy/internal/protocol"
)

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
	raw := strings.TrimSpace(options.Codec)
	if strings.EqualFold(raw, protocol.DesktopCodecH265Validation) {
		// Preserve the diagnostics-only sentinel. It intentionally bypasses
		// public capability negotiation so validation can exercise fallback.
		return options, nil
	}

	preference := desktopcodec.NormalizeCodecPreference(raw)
	options.Codec = preference
	if preference != "h265" {
		return options, nil
	}

	targetCodec, ok := desktopCodecCapability(target.Capabilities.Codecs, "h265")
	if !ok || !targetCodec.Encode {
		return options, fmt.Errorf("Relay Desktop target does not advertise H.265 encode support")
	}
	localCodec, ok := desktopCodecCapability(localCapabilities, "h265")
	if !ok || !localCodec.Decode {
		return options, fmt.Errorf("this device does not advertise H.265 decode support")
	}
	return options, nil
}
