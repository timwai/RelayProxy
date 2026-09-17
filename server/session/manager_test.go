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
