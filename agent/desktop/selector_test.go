package desktop

import (
	"errors"
	"testing"

	"relayproxy/internal/protocol"
)

func TestSelectBackend(t *testing.T) {
	tests := []struct {
		name    string
		target  protocol.DesktopCapabilities
		options protocol.RemoteDesktopConnectOptions
		want    protocol.DesktopBackend
		wantErr bool
	}{
		{
			name:   "current RDP-only target remains usable in auto mode",
			target: protocol.DesktopCapabilities{NativeRDP: true},
			want:   protocol.DesktopBackendRDP,
		},
		{
			name:   "auto prefers relay desktop outside office scene",
			target: protocol.DesktopCapabilities{NativeRDP: true, RelayDesktop: true},
			want:   protocol.DesktopBackendRelay,
		},
		{
			name:    "office prefers native RDP",
			target:  protocol.DesktopCapabilities{NativeRDP: true, RelayDesktop: true},
			options: protocol.RemoteDesktopConnectOptions{Scene: protocol.DesktopSceneOffice},
			want:    protocol.DesktopBackendRDP,
		},
		{
			name:    "explicit unavailable relay does not silently fall back",
			target:  protocol.DesktopCapabilities{NativeRDP: true},
			options: protocol.RemoteDesktopConnectOptions{Backend: protocol.DesktopBackendRelay},
			wantErr: true,
		},
		{
			name:    "target without backend is rejected",
			target:  protocol.DesktopCapabilities{},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := protocol.RemoteDesktopTarget{DeviceID: "dev-test", Capabilities: tc.target}
			got, err := SelectBackend(target, tc.options)
			if tc.wantErr {
				if !errors.Is(err, ErrBackendUnavailable) {
					t.Fatalf("SelectBackend() error = %v, want ErrBackendUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectBackend() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("SelectBackend() = %q, want %q", got, tc.want)
			}
		})
	}
}
