package app

import "testing"

func TestPublicIdentityIDDoesNotRequireVerifiedTransport(t *testing.T) {
	for _, cfg := range []AgentConfig{
		{IdentityID: "team-test", PlainTCP: true},
		{IdentityID: "team-test", InsecureTLS: true},
	} {
		if _, err := NewAgent(cfg); err != nil {
			t.Fatalf("public identity id rejected: %v", err)
		}
	}
}
