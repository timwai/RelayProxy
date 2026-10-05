package protocol

import "testing"

func TestProxyPathKinds(t *testing.T) {
	tests := []struct {
		path       ProxyPath
		wantDirect bool
		wantRelay  bool
	}{
		{ProxyPathPublicDirectQUIC, true, false},
		{ProxyPathP2PQUIC, true, false},
		{ProxyPathRelayQUIC, false, true},
		{ProxyPathRelayTLS, false, true},
		{"", false, false},
		{"unknown", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.path.String(), func(t *testing.T) {
			if got := tt.path.IsDirect(); got != tt.wantDirect {
				t.Fatalf("IsDirect() = %t, want %t", got, tt.wantDirect)
			}
			if got := tt.path.IsRelay(); got != tt.wantRelay {
				t.Fatalf("IsRelay() = %t, want %t", got, tt.wantRelay)
			}
		})
	}
}

func TestLegacyP2PPathAliases(t *testing.T) {
	if P2PPathDirectQUIC != ProxyPathP2PQUIC.String() {
		t.Fatalf("P2P direct alias = %q, want %q", P2PPathDirectQUIC, ProxyPathP2PQUIC)
	}
	if P2PPathRelayQUIC != ProxyPathRelayQUIC.String() {
		t.Fatalf("Relay QUIC alias = %q, want %q", P2PPathRelayQUIC, ProxyPathRelayQUIC)
	}
	if P2PPathRelayTLS != ProxyPathRelayTLS.String() {
		t.Fatalf("Relay TLS alias = %q, want %q", P2PPathRelayTLS, ProxyPathRelayTLS)
	}
}
