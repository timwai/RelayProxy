package app

import (
	"strings"
	"testing"

	"relayproxy/agent/desktop"
)

func TestNewRemoteDesktopSessionID(t *testing.T) {
	first, err := newRemoteDesktopSessionID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRemoteDesktopSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "desktop_") || len(first) <= len("desktop_") {
		t.Fatalf("unexpected desktop session id %q", first)
	}
	if first == second {
		t.Fatalf("desktop session ids collided: %q", first)
	}
}

func TestTakeDesktopSessionsLockedDeduplicatesPrimary(t *testing.T) {
	primary := &desktop.ControllerSession{}
	secondary := &desktop.ControllerSession{}
	a := &Agent{
		desktopConnection:       primary,
		desktopPrimarySessionID: "primary",
		desktopConnections: map[string]*desktop.ControllerSession{
			"primary":   primary,
			"secondary": secondary,
		},
	}

	a.mu.Lock()
	got := a.takeDesktopSessionsLocked()
	a.mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("taken desktop sessions=%d want=2", len(got))
	}
	if a.desktopConnection != nil || a.desktopPrimarySessionID != "" || len(a.desktopConnections) != 0 {
		t.Fatalf("desktop registry not cleared: primary=%v id=%q map=%d",
			a.desktopConnection != nil, a.desktopPrimarySessionID, len(a.desktopConnections))
	}
}

func TestRemovePrimaryDesktopSessionPromotesRemainingSession(t *testing.T) {
	primary := &desktop.ControllerSession{}
	secondaryA := &desktop.ControllerSession{}
	secondaryB := &desktop.ControllerSession{}
	a := &Agent{
		desktopConnection:       primary,
		desktopPrimarySessionID: "primary",
		desktopConnections: map[string]*desktop.ControllerSession{
			"primary": primary,
			"b":       secondaryB,
			"a":       secondaryA,
		},
	}
	a.removeDesktopSession("primary", primary)
	if a.desktopPrimarySessionID != "a" || a.desktopConnection != secondaryA {
		t.Fatalf("promoted primary id=%q session=%p want id=a session=%p",
			a.desktopPrimarySessionID, a.desktopConnection, secondaryA)
	}
	if _, ok := a.desktopConnections["primary"]; ok {
		t.Fatal("closed primary remained in desktop registry")
	}
}

func TestDesktopP2PEligibleIsScopedToLogicalSession(t *testing.T) {
	primary := &desktop.ControllerSession{}
	secondary := &desktop.ControllerSession{}
	a := &Agent{
		desktopConnection:       primary,
		desktopPrimarySessionID: "primary",
		desktopConnections: map[string]*desktop.ControllerSession{
			"primary":   primary,
			"secondary": secondary,
		},
	}
	if !a.desktopP2PEligible("primary", primary) {
		t.Fatal("registered primary Relay Desktop stream should be P2P eligible")
	}
	if !a.desktopP2PEligible("secondary", secondary) {
		t.Fatal("registered secondary Relay Desktop stream should be independently P2P eligible")
	}
	if a.desktopP2PEligible("primary", secondary) {
		t.Fatal("P2P eligibility crossed logical desktop sessions")
	}
	if a.desktopP2PEligible("missing", primary) {
		t.Fatal("unregistered logical desktop session became P2P eligible")
	}
}
