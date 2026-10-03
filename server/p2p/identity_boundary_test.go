package p2p

import (
	"relayproxy/server/session"
	"testing"
)

func TestIdentityPairIsolationAndLivePolicy(t *testing.T) {
	allowed := true
	c := &Coordinator{authorize: func(string, string) (bool, error) { return allowed, nil }}
	client := &session.DeviceSession{DeviceID: "client", OwnerUserID: "same-admin", IdentityID: "identity"}
	exit := &session.DeviceSession{DeviceID: "exit", OwnerUserID: "same-admin"}
	if ok, err := c.authorizedPair(client, exit); err != nil || ok {
		t.Fatal("mixed models allowed", ok, err)
	}
	client.IdentityID, exit.IdentityID = "", "identity"
	if ok, err := c.authorizedPair(client, exit); err != nil || ok {
		t.Fatal("reverse mixed models allowed", ok, err)
	}
	client.IdentityID = "identity"
	if ok, err := c.authorizedPair(client, exit); err != nil || !ok {
		t.Fatal(ok, err)
	}
	allowed = false
	if ok, err := c.authorizedPair(client, exit); err != nil || ok {
		t.Fatal("cached identity bypassed live policy", ok, err)
	}
}
