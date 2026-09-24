package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestDisplayCapabilitiesSnapshotTracksLiveTopology(t *testing.T) {
	session := &ControllerSession{}
	if displays, ready := session.DisplayCapabilitiesSnapshot(); ready || displays != nil {
		t.Fatalf("uninitialized displays=%+v ready=%v", displays, ready)
	}

	input := []protocol.DesktopDisplayCapability{
		{ID: "10", Name: "Primary", Width: 1920, Height: 1080, Primary: true},
		{ID: "20", Name: "Secondary", Width: 2560, Height: 1440},
	}
	session.applyDisplayCapabilities(input)
	input[0].Name = "Mutated"

	got, ready := session.DisplayCapabilitiesSnapshot()
	if !ready || len(got) != 2 || got[0].Name != "Primary" {
		t.Fatalf("live displays=%+v ready=%v", got, ready)
	}
	got[0].Name = "Caller mutation"
	again, ready := session.DisplayCapabilitiesSnapshot()
	if !ready || again[0].Name != "Primary" {
		t.Fatalf("display snapshot returned internal storage: %+v", again)
	}

	session.applyDisplayCapabilities(nil)
	empty, ready := session.DisplayCapabilitiesSnapshot()
	if !ready || len(empty) != 0 {
		t.Fatalf("empty live topology=%+v ready=%v", empty, ready)
	}
}
