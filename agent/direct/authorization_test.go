package direct

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestAuthorizationStoreTracksAndRevokesCurrentRevision(t *testing.T) {
	store := NewAuthorizationStore("exit", 7)
	if err := store.Update(protocol.PublicDirectAuthorizationUpdate{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 7, AuthorizationRevision: 11, Authorized: true,
	}); err != nil {
		t.Fatal(err)
	}
	if revision, ok := store.Resolve("client"); !ok || revision != 11 {
		t.Fatalf("revision=%d ok=%v", revision, ok)
	}
	if err := store.Update(protocol.PublicDirectAuthorizationUpdate{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 7, AuthorizationRevision: 11, Authorized: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Resolve("client"); ok {
		t.Fatal("revoked client remained authorized")
	}
}

func TestAuthorizationStoreRejectsStalePolicyAndWrongExit(t *testing.T) {
	store := NewAuthorizationStore("exit", 7)
	for _, update := range []protocol.PublicDirectAuthorizationUpdate{
		{ClientDeviceID: "client", ExitDeviceID: "other", PolicyRevision: 7, Authorized: true},
		{ClientDeviceID: "client", ExitDeviceID: "exit", PolicyRevision: 8, Authorized: true},
	} {
		if err := store.Update(update); err == nil {
			t.Fatalf("stale update accepted: %+v", update)
		}
	}
	if _, ok := store.Resolve("client"); ok {
		t.Fatal("rejected update changed authorization state")
	}
}
