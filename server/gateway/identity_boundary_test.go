package gateway

import (
	"relayproxy/server/session"
	"testing"
)

func TestExitIdentityBoundaryNeverFallsBackToLegacyOwner(t *testing.T) {
	for _, ids := range [][2]string{{"identity", ""}, {"", "identity"}} {
		router := NewStreamRouter(session.NewManager(), nil, func(string, string) (bool, error) { t.Fatal("mixed models reached fallback"); return true, nil }, nil)
		client := &session.DeviceSession{DeviceID: "client", OwnerUserID: "same-admin", IdentityID: ids[0]}
		exit := &session.DeviceSession{DeviceID: "exit", OwnerUserID: "same-admin", IdentityID: ids[1]}
		if ok, err := router.authorizeExit(client, exit); err != nil || ok {
			t.Fatalf("mixed models allowed: %v %v", ok, err)
		}
	}
}

func TestSameIdentityExitUsesCurrentPolicy(t *testing.T) {
	allowed := true
	router := NewStreamRouter(session.NewManager(), nil, func(string, string) (bool, error) { return allowed, nil }, nil)
	client := &session.DeviceSession{DeviceID: "client", IdentityID: "identity"}
	exit := &session.DeviceSession{DeviceID: "exit", IdentityID: "identity"}
	if ok, err := router.authorizeExit(client, exit); err != nil || !ok {
		t.Fatal(ok, err)
	}
	allowed = false
	if ok, err := router.authorizeExit(client, exit); err != nil || ok {
		t.Fatal("cached identity bypassed live policy", ok, err)
	}
}
