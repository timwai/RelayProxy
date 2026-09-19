package gateway

import (
	"context"
	"testing"

	"relayproxy/internal/acl"

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


func BenchmarkTargetPolicyCachedSnapshot(b *testing.B) {
	checker, err := acl.NewChecker(acl.Policy{
		ID:            "relay",
		AllowInternet: true,
		Rules: []acl.Rule{{
			Priority: 10, Action: acl.ActionDeny, Protocol: "tcp",
			TargetType: acl.TargetDomain, TargetValue: "blocked.example",
		}},
	})
	if err != nil {
		b.Fatal(err)
	}
	router := NewStreamRouter(session.NewManager(), checker, nil, nil)
	exit := &session.DeviceSession{Capabilities: []string{protocol.CapabilityTargetACL}}
	first, err := router.targetPolicy(context.Background(), exit, "example.com", 443, "tcp")
	if err != nil {
		b.Fatal(err)
	}
	second, err := router.targetPolicy(context.Background(), exit, "example.com", 443, "tcp")
	if err != nil {
		b.Fatal(err)
	}
	if first == nil || first != second {
		b.Fatal("relay policy snapshot is not reused")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := router.targetPolicy(context.Background(), exit, "example.com", 443, "tcp"); err != nil {
			b.Fatal(err)
		}
	}
}
