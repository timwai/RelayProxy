package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestRDPTargetsFromProtocolFiltersInvalidEntriesAndKeepsEmptyArray(t *testing.T) {
	got := rdpTargetsFromProtocol([]protocol.RDPTarget{
		{DeviceID: ""},
		{DeviceID: "target", Name: "Office PC", Online: true},
	})
	if len(got) != 1 || got[0].DeviceID != "target" || got[0].Name != "Office PC" || !got[0].Online {
		t.Fatalf("converted targets = %+v", got)
	}
	if empty := rdpTargetsFromProtocol(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty targets must remain a non-nil array: %#v", empty)
	}
}
