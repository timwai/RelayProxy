package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestFilterAccessibleFileSharePeersFailsClosed(t *testing.T) {
	candidates := []protocol.FileSharePeer{
		{DeviceID: "unshared", Name: "Unshared", Online: true},
		{DeviceID: "granted", Name: "Granted", Online: true},
		{DeviceID: "unreachable", Online: true},
		{DeviceID: "disabled", Online: true},
		{DeviceID: "invalid-permission", Online: true},
		{DeviceID: "offline", Online: false},
		{DeviceID: "granted", Online: true}, // stale duplicate from discovery
	}
	var mu sync.Mutex
	called := map[string]int{}
	list := func(_ context.Context, id string) ([]protocol.FileShareDescriptor, error) {
		mu.Lock()
		called[id]++
		mu.Unlock()
		switch id {
		case "granted":
			return []protocol.FileShareDescriptor{{ID: "share1", Permission: "read"}}, nil
		case "unreachable":
			return nil, errors.New("host went offline")
		case "invalid-permission":
			return []protocol.FileShareDescriptor{{ID: "share2", Permission: "none"}}, nil
		default:
			return []protocol.FileShareDescriptor{}, nil
		}
	}
	got := filterAccessibleFileSharePeers(context.Background(), candidates, list)
	if len(got) != 1 || got[0].DeviceID != "granted" || len(got[0].Shares) != 1 {
		t.Fatalf("unapproved files.host peer leaked into discovered devices: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if called["offline"] != 0 || called["granted"] != 1 {
		t.Fatalf("checked offline or duplicate host: %+v", called)
	}
}

func TestFilterAccessibleFileSharePeersHonorsRevocationAndDeadline(t *testing.T) {
	peers := []protocol.FileSharePeer{{DeviceID: "host", Online: true}}
	grant := true
	list := func(_ context.Context, _ string) ([]protocol.FileShareDescriptor, error) {
		if !grant {
			return nil, nil
		}
		return []protocol.FileShareDescriptor{{ID: "share", Permission: "read_write"}}, nil
	}
	if got := filterAccessibleFileSharePeers(context.Background(), peers, list); len(got) != 1 {
		t.Fatalf("authorized host missing: %+v", got)
	}
	grant = false
	if got := filterAccessibleFileSharePeers(context.Background(), peers, list); len(got) != 0 {
		t.Fatalf("host remains visible after local ACL removal: %+v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	list = func(ctx context.Context, _ string) ([]protocol.FileShareDescriptor, error) {
		<-ctx.Done()
		return []protocol.FileShareDescriptor{{ID: "stale", Permission: "read"}}, nil
	}
	if got := filterAccessibleFileSharePeers(ctx, peers, list); len(got) != 0 {
		t.Fatalf("late stale ACL response should fail closed: %+v", got)
	}
}
