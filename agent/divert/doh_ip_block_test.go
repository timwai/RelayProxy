package divert

import (
	"testing"
)

func TestConfiguredDoHIPBlocksOnlyUserSelectedEndpoints(t *testing.T) {
	entries := []string{"9.9.9.9", "1.1.1.0/24", "2606:4700:4700::/48"}
	for _, ip := range []string{"9.9.9.9", "1.1.1.1", "1.1.1.200", "2606:4700:4700::1111"} {
		if !matchesConfiguredDoHIP(ip, entries) {
			t.Errorf("configured DoH endpoint was not recognized: %s", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.2.1", "2606:4700:4701::1", "invalid", "127.0.0.1"} {
		if matchesConfiguredDoHIP(ip, entries) {
			t.Errorf("unrelated destination was blocked: %s", ip)
		}
	}
}

func TestDoHIPPolicyRespectsHotEnableAndTCPUDP443Only(t *testing.T) {
	enabled := true
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return true },
		DoHBlockedIPs: func() []string {
			if !enabled {
				return nil
			}
			return []string{"9.9.9.9"}
		},
	})
	for _, protocol := range []Protocol{ProtoTCP, ProtoUDP} {
		for _, tc := range []struct {
			ip     string
			port   uint16
			action Action
		}{
			{"9.9.9.9", 443, ActionReject},
			{"9.9.9.9", 8443, ActionProxy},
			{"8.8.8.8", 443, ActionProxy},
		} {
			f := testFlow(protocol, nil)
			f.IP, f.Port = tc.ip, tc.port
			r, err := s.ClassifyFlow(f)
			if err != nil {
				t.Fatal(err)
			}
			if r.Decision().Action != tc.action {
				t.Errorf("%s %s:%d got %s want %s", protocol, tc.ip, tc.port, r.Decision().Action, tc.action)
			}
		}
	}
	enabled = false
	f := testFlow(ProtoTCP, nil)
	f.IP = "9.9.9.9"
	r, err := s.ClassifyFlow(f)
	if err != nil || r.Decision().Action != ActionProxy {
		t.Fatalf("disabled DoH IP guard unexpectedly blocked traffic: %v %+v", err, r)
	}
}
