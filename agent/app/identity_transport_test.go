package app

import "testing"

func TestPublicIdentityIDDoesNotRequireVerifiedTransport(t *testing.T) {
	for _, cfg := range []AgentConfig{
		{IdentityID: "a1b2c3d4e5f6g7h8", PlainTCP: true},
		{IdentityID: "a1b2c3d4e5f6g7h8", InsecureTLS: true},
	} {
		if _, err := NewAgent(cfg); err != nil {
			t.Fatalf("public identity id rejected: %v", err)
		}
	}
}
