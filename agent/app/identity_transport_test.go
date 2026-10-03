package app

import (
	"strings"
	"testing"
)

func TestIdentityKeyRejectsUnverifiedTransportBeforeSetup(t *testing.T) {
	for _, cfg := range []AgentConfig{
		{AccessKey: "rpk_test", PlainTCP: true},
		{AccessKey: "rpk_test", InsecureTLS: true},
	} {
		if _, err := NewAgent(cfg); err == nil || !strings.Contains(err.Error(), "certificate verification") {
			t.Fatalf("credential transport accepted: %v", err)
		}
	}
}
