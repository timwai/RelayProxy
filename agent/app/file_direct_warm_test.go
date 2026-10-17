package app

import (
	"context"
	"testing"
	"time"
)

func TestPrewarmRequiresAuthenticatedFileSharingSession(t *testing.T) {
	agent := &Agent{}
	if _, err := agent.PrewarmRemoteFileDirect(context.Background(), "peer", "share"); err == nil {
		t.Fatal("warmup without files.client capability or Relay session must fail")
	}
	for _, target := range [][2]string{{"", "share"}, {"peer", ""}, {"peer", "    "}} {
		if _, err := agent.PrewarmRemoteFileDirect(context.Background(), target[0], target[1]); err == nil {
			t.Fatalf("invalid warmup target accepted: %q %q", target[0], target[1])
		}
	}
}

func TestFileWarmHintsAreScopedToDeviceShareAndSessionEpoch(t *testing.T) {
	agent := &Agent{epoch: 17}
	agent.fileWarmHints = map[string]fileDirectWarmHint{
		"host\x00share":    {state: "relay", epoch: 17, until: time.Now().Add(time.Second)},
		"host\x00other":    {state: "ready", epoch: 17, until: time.Now().Add(time.Second)},
		"expired\x00share": {state: "relay", epoch: 17, until: time.Now().Add(-time.Second)},
		"prior\x00share":   {state: "relay", epoch: 16, until: time.Now().Add(time.Minute)},
	}
	if !agent.fileDirectRecentlyUnreachable("host", "share") {
		t.Fatal("failed direct probe not respected")
	}
	for _, scope := range [][2]string{{"host", "other"}, {"expired", "share"}, {"prior", "share"}, {"stranger", "share"}} {
		if agent.fileDirectRecentlyUnreachable(scope[0], scope[1]) {
			t.Fatalf("unrelated, expired or prior-session hint suppressed direct: %v", scope)
		}
	}
	agent.mu.Lock()
	agent.epoch = 18
	agent.mu.Unlock()
	if agent.fileDirectRecentlyUnreachable("host", "share") {
		t.Fatal("stale epoch suppressed a new Relay session")
	}
}

func TestFileWarmPathHintNeedsFreshCurrentSessionAndExpiry(t *testing.T) {
	agent := &Agent{epoch: 3, fileWarmHints: map[string]fileDirectWarmHint{
		"host\x00share":    {state: "ready", path: "direct_tls", epoch: 3, until: time.Now().Add(time.Minute)},
		"host\x00other":    {state: "relay", epoch: 3, until: time.Now().Add(time.Minute)},
		"old\x00share":     {state: "ready", path: "direct_quic", epoch: 2, until: time.Now().Add(time.Minute)},
		"expired\x00share": {state: "ready", path: "direct_quic", epoch: 3, until: time.Now().Add(-time.Minute)},
	}}
	if got := agent.fileDirectWarmPath("host", "share"); got != "direct_tls" {
		t.Fatalf("valid path hint: %q", got)
	}
	for _, scope := range [][2]string{{"host", "other"}, {"old", "share"}, {"expired", "share"}} {
		if got := agent.fileDirectWarmPath(scope[0], scope[1]); got != "" {
			t.Fatalf("untrusted hint %v: %q", scope, got)
		}
	}
	agent.mu.Lock()
	agent.epoch = 4
	agent.mu.Unlock()
	if got := agent.fileDirectWarmPath("host", "share"); got != "" {
		t.Fatalf("reconnected session reused stale path: %q", got)
	}
}
