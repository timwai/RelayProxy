package session

import "testing"

import "relayproxy/internal/protocol"

func TestUnregisterReportsCurrentGenerationOnly(t *testing.T) {
	m := NewManager()
	old := &DeviceSession{DeviceID: "device"}
	current := &DeviceSession{DeviceID: "device"}
	m.Register(old)
	m.Register(current)
	if m.UnregisterSession(old) {
		t.Fatal("late old unregister reported removal")
	}
	if got, ok := m.Get("device"); !ok || got != current {
		t.Fatal("late old unregister removed current session")
	}
	if !m.UnregisterSession(current) {
		t.Fatal("current unregister did not report removal")
	}
}

func TestIsExitRequiresCapability(t *testing.T) {
	cases := []struct {
		name string
		mode string
		caps []string
		want bool
	}{
		{"exit mode with exit cap", "EXIT", []string{"tcp", protocol.CapabilityProxyExit}, true},
		{"both mode with exit cap", "BOTH", []string{protocol.CapabilityProxyExit}, true},
		{"both without exit cap", "BOTH", []string{"tcp", "socks5"}, false},
		{"client with server exit grant", "CLIENT", []string{protocol.CapabilityProxyExit}, true},
		{"empty grants cannot exit", "BOTH", nil, false},
		{"legacy empty caps client", "CLIENT", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &DeviceSession{Mode: tc.mode, Grants: tc.caps}
			if got := s.IsExit(); got != tc.want {
				t.Fatalf("IsExit()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestDesktopCapabilitiesForTargetUsesAuthorizedOnlineSnapshot(t *testing.T) {
	sess := &DeviceSession{
		Grants: []string{protocol.CapabilityDesktopHost},
		DesktopCapabilities: protocol.DesktopCapabilities{
			RelayDesktop: true,
			Captures: []protocol.DesktopCaptureCapability{{Backend: "dxgi", Cursor: true}},
			Codecs: []protocol.DesktopCodecCapability{{Codec: "h264", Encode: true}},
			Displays: []protocol.DesktopDisplayCapability{{ID: "10", Name: "DISPLAY1", Width: 1920, Height: 1080, Primary: true}},
			MultiMonitor: false,
			MaxWidth: 3840, MaxHeight: 2160, MaxFPS: 30,
		},
	}
	got := DesktopCapabilitiesForTarget(sess, true, true)
	if !got.NativeRDP || !got.RelayDesktop || len(got.Displays) != 1 || got.Displays[0].ID != "10" {
		t.Fatalf("merged capabilities=%+v", got)
	}
	got.Displays[0].ID = "mutated"
	if sess.DesktopCapabilities.Displays[0].ID != "10" {
		t.Fatal("returned display slice aliases authenticated session snapshot")
	}
}

func TestDesktopCapabilitiesForTargetDoesNotLeakUnusableSnapshot(t *testing.T) {
	sess := &DeviceSession{
		Grants: []string{protocol.CapabilityRDPHost},
		DesktopCapabilities: protocol.DesktopCapabilities{
			RelayDesktop: true,
			Displays: []protocol.DesktopDisplayCapability{{ID: "secret-display", Width: 1920, Height: 1080}},
		},
	}
	got := DesktopCapabilitiesForTarget(sess, true, true)
	if !got.NativeRDP || !got.RelayDesktop {
		t.Fatalf("backend authorization flags lost: %+v", got)
	}
	if len(got.Displays) != 0 || len(got.Codecs) != 0 || len(got.Captures) != 0 {
		t.Fatalf("dynamic desktop details leaked without current desktop.host grant: %+v", got)
	}

	got = DesktopCapabilitiesForTarget(sess, true, false)
	if got.RelayDesktop || len(got.Displays) != 0 {
		t.Fatalf("relay details leaked to native-RDP-only target: %+v", got)
	}
}
