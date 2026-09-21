package desktop

import (
	"errors"
	"fmt"

	"relayproxy/internal/protocol"
)

var ErrBackendUnavailable = errors.New("remote desktop backend is unavailable")

// SelectBackend chooses the implementation without performing any network or
// UI work. Explicit user choices are strict; only auto mode may fall back.
func SelectBackend(target protocol.RemoteDesktopTarget, options protocol.RemoteDesktopConnectOptions) (protocol.DesktopBackend, error) {
	backend := options.Backend
	if backend == "" {
		backend = protocol.DesktopBackendAuto
	}
	switch backend {
	case protocol.DesktopBackendRDP:
		if target.Capabilities.NativeRDP {
			return protocol.DesktopBackendRDP, nil
		}
		return "", fmt.Errorf("%w: native RDP is unavailable", ErrBackendUnavailable)
	case protocol.DesktopBackendRelay:
		if target.Capabilities.RelayDesktop {
			return protocol.DesktopBackendRelay, nil
		}
		return "", fmt.Errorf("%w: Relay Desktop is unavailable", ErrBackendUnavailable)
	case protocol.DesktopBackendAuto:
	default:
		return "", fmt.Errorf("unsupported remote desktop backend %q", backend)
	}

	scene := options.Scene
	if scene == "" {
		scene = protocol.DesktopSceneAuto
	}
	if scene == protocol.DesktopSceneOffice && target.Capabilities.NativeRDP {
		return protocol.DesktopBackendRDP, nil
	}
	if target.Capabilities.RelayDesktop {
		return protocol.DesktopBackendRelay, nil
	}
	if target.Capabilities.NativeRDP {
		return protocol.DesktopBackendRDP, nil
	}
	return "", fmt.Errorf("%w: target exposes no supported backend", ErrBackendUnavailable)
}
