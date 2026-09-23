package gui

import (
	"strings"

	"relayproxy/internal/protocol"
)

const remoteDesktopHEVCValidationEnv = "RELAYPROXY_DESKTOP_HEVC_VALIDATION"

func remoteDesktopHEVCValidationEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func applyRemoteDesktopHEVCValidationOptions(
	options protocol.RemoteDesktopConnectOptions,
	envValue string,
) protocol.RemoteDesktopConnectOptions {
	if !remoteDesktopHEVCValidationEnabled(envValue) {
		return options
	}
	options.Backend = protocol.DesktopBackendRelay
	options.Codec = protocol.DesktopCodecH265Validation
	return options
}
