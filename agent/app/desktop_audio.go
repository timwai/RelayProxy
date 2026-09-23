package app

import (
	"fmt"
	"strings"

	"relayproxy/internal/protocol"
)

func targetSupportsDesktopAudioCodec(caps protocol.DesktopCapabilities, codec string) bool {
	codec = strings.TrimSpace(strings.ToLower(codec))
	if !caps.Audio || codec == "" {
		return false
	}
	if len(caps.AudioCodecs) == 0 {
		// Audio capability predates explicit codec negotiation. Legacy Relay
		// Desktop audio is PCM S16LE, so preserve that as the compatibility
		// default and never assume an old peer can decode Opus.
		return codec == protocol.DesktopAudioCodecPCMS16LE
	}
	for _, candidate := range caps.AudioCodecs {
		if strings.EqualFold(strings.TrimSpace(candidate), codec) {
			return true
		}
	}
	return false
}

func negotiateRemoteDesktopAudio(
	target protocol.RemoteDesktopTarget,
	options protocol.RemoteDesktopConnectOptions,
) (protocol.RemoteDesktopConnectOptions, error) {
	if options.Audio != nil && !*options.Audio {
		options.AudioCodec = ""
		return options, nil
	}
	if !target.Capabilities.Audio {
		options.AudioCodec = ""
		return options, nil
	}

	requested := strings.TrimSpace(strings.ToLower(options.AudioCodec))
	if requested != "" {
		switch requested {
		case protocol.DesktopAudioCodecOpus, protocol.DesktopAudioCodecPCMS16LE:
		default:
			return options, fmt.Errorf("unsupported Relay Desktop audio codec %q", options.AudioCodec)
		}
		if !targetSupportsDesktopAudioCodec(target.Capabilities, requested) {
			return options, fmt.Errorf("Relay Desktop target does not support audio codec %q", requested)
		}
		options.AudioCodec = requested
		return options, nil
	}

	if targetSupportsDesktopAudioCodec(target.Capabilities, protocol.DesktopAudioCodecOpus) {
		options.AudioCodec = protocol.DesktopAudioCodecOpus
	} else {
		options.AudioCodec = protocol.DesktopAudioCodecPCMS16LE
	}
	return options, nil
}
