package rdp

import "testing"

func TestRDPFriendlyHostsNameKeepsLoopbackListenerAndPort(t *testing.T) {
	conn := &Connection{ListenAddr: "127.0.0.1:41389", HostAlias: "office-pc-01234567.relayproxy.test"}
	if conn.Addr() != "127.0.0.1:41389" {
		t.Fatalf("listener should stay on loopback, got %q", conn.Addr())
	}
	if got := conn.ClientAddress(); got != "office-pc-01234567.relayproxy.test:41389" {
		t.Fatalf("desktop address = %q", got)
	}
	conn.HostAlias = ""
	if got := conn.ClientAddress(); got != "127.0.0.1:41389" {
		t.Fatalf("fallback address = %q", got)
	}
}
