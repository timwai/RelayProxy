package app

import "testing"

func TestInsecureTransportRequiresExplicitDevelopmentOptIn(t *testing.T) {
	for _, cfg := range []AgentConfig{
		{IdentityID: "a1b2c3d4e5f6g7h8", PlainTCP: true},
		{IdentityID: "a1b2c3d4e5f6g7h8", InsecureTLS: true},
	} {
		if _, err := NewAgent(cfg); err == nil {
			t.Fatal("insecure transport was accepted without an explicit development opt-in")
		}
	}
	if _, err := NewAgent(AgentConfig{IdentityID: "a1b2c3d4e5f6g7h8", PlainTCP: true, AllowPlainTCP: true}); err != nil {
		t.Fatalf("explicit plaintext development opt-in rejected: %v", err)
	}
	if _, err := NewAgent(AgentConfig{IdentityID: "a1b2c3d4e5f6g7h8", InsecureTLS: true, AllowInsecureTLS: true}); err != nil {
		t.Fatalf("explicit insecure TLS development opt-in rejected: %v", err)
	}
}
