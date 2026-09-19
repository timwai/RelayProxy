package gateway

import (
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func TestAuthorizeExitUsesAuthenticatedOwnerSnapshot(t *testing.T) {
	manager := session.NewManager()
	fallbackCalls := 0
	router := NewStreamRouter(manager, nil, func(string, string) (bool, error) {
		fallbackCalls++
		return false, nil
	}, nil)
	client := &session.DeviceSession{DeviceID: "client", OwnerUserID: "owner-1"}
	exit := &session.DeviceSession{
		DeviceID: "exit", OwnerUserID: "owner-1",
		Grants: []string{protocol.CapabilityProxyExit},
	}
	ok, err := router.authorizeExit(client, exit)
	if err != nil || !ok {
		t.Fatalf("cached authorization failed: ok=%v err=%v", ok, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("database fallback called %d times", fallbackCalls)
	}

	exit.OwnerUserID = "owner-2"
	ok, err = router.authorizeExit(client, exit)
	if err != nil || ok {
		t.Fatalf("cross-owner exit was authorized: ok=%v err=%v", ok, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("database fallback called for complete ownership snapshots")
	}
}

func BenchmarkAuthorizeExitSessionSnapshot(b *testing.B) {
	router := NewStreamRouter(session.NewManager(), nil, nil, nil)
	client := &session.DeviceSession{DeviceID: "client", OwnerUserID: "owner"}
	exit := &session.DeviceSession{DeviceID: "exit", OwnerUserID: "owner"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ok, err := router.authorizeExit(client, exit)
		if err != nil || !ok {
			b.Fatal("authorization failed")
		}
	}
}
