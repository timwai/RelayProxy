package protocol

import "testing"

func TestProxyPathClassification(t *testing.T) {
	for _, path := range []ProxyPath{ProxyPathPublicDirectQUIC, ProxyPathP2PQUIC} {
		if !path.IsDirect() || path.IsRelay() {
			t.Fatalf("direct path classification failed for %q", path)
		}
	}
	for _, path := range []ProxyPath{ProxyPathRelayQUIC, ProxyPathRelayTLS} {
		if path.IsDirect() || !path.IsRelay() {
			t.Fatalf("relay path classification failed for %q", path)
		}
	}
	if P2PPathDirectQUIC != ProxyPathP2PQUIC.String() ||
		P2PPathRelayQUIC != ProxyPathRelayQUIC.String() ||
		P2PPathRelayTLS != ProxyPathRelayTLS.String() {
		t.Fatal("legacy P2P path aliases diverged from the path-neutral model")
	}
}
