package session

import (
	"bytes"
	"encoding/json"
	"testing"

	"relayproxy/internal/protocol"
)

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

func TestRuntimeCapabilityKeepsControlSessionOutOfExitInventory(t *testing.T) {
	control := &DeviceSession{
		Grants:              []string{protocol.CapabilityProxyClient, protocol.CapabilityProxyExit},
		RuntimeCapabilities: []string{},
	}
	if control.IsExit() {
		t.Fatal("control-only session was advertised as an online exit")
	}
	control.RuntimeCapabilities = []string{protocol.CapabilityProxyExit}
	if !control.IsExit() {
		t.Fatal("active exit session was omitted from the exit inventory")
	}
}

func TestDeviceDiagnosticsValidateAndClonePayload(t *testing.T) {
	session := &DeviceSession{}
	payload := json.RawMessage(`{"sampledAt":"2026-10-03T00:00:00Z","mode":"BOTH"}`)
	session.SetDiagnostics(payload)
	payload[2] = 'X'

	first := session.DiagnosticsSnapshot()
	if first == nil || !json.Valid(first.Payload) || bytes.Contains(first.Payload, []byte("XampledAt")) {
		t.Fatalf("stored diagnostics aliases caller payload: %s", first.Payload)
	}
	first.Payload[2] = 'Y'
	second := session.DiagnosticsSnapshot()
	if bytes.Contains(second.Payload, []byte("YampledAt")) {
		t.Fatal("diagnostics snapshot aliases stored payload")
	}
	if second.Active {
		t.Fatal("inactive diagnostics were marked active")
	}
	session.SetDiagnostics(json.RawMessage(`{"connections":[{"state":"active"}]}`))
	if active := session.DiagnosticsSnapshot(); active == nil || !active.Active {
		t.Fatalf("active peer connection was not detected: %+v", active)
	}
	session.SetDiagnostics(second.Payload)

	session.SetDiagnostics(json.RawMessage(`{invalid`))
	session.SetDiagnostics(bytes.Repeat([]byte(" "), 256*1024+1))
	if got := session.DiagnosticsSnapshot(); got == nil || !bytes.Equal(got.Payload, second.Payload) {
		t.Fatalf("invalid or oversized payload replaced last valid diagnostics: %+v", got)
	}
}
