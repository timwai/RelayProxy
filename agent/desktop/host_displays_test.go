package desktop

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestSameDesktopDisplaysDetectsTopologyChanges(t *testing.T) {
	base := []protocol.DesktopDisplayCapability{
		{ID: "10", Name: "Primary", Width: 1920, Height: 1080, RefreshHz: 60, Primary: true},
		{ID: "20", Name: "Secondary", Width: 2560, Height: 1440, RefreshHz: 144},
	}
	if !sameDesktopDisplays(base, cloneDesktopDisplays(base)) {
		t.Fatal("identical display topology was reported as changed")
	}

	changed := cloneDesktopDisplays(base)
	changed[1].Width = 1920
	if sameDesktopDisplays(base, changed) {
		t.Fatal("display resolution change was not detected")
	}

	changed = cloneDesktopDisplays(base)
	changed[0].Primary = false
	changed[1].Primary = true
	if sameDesktopDisplays(base, changed) {
		t.Fatal("primary display change was not detected")
	}

	if sameDesktopDisplays(base, base[:1]) {
		t.Fatal("display removal was not detected")
	}
}

func TestCloneDesktopDisplaysDoesNotAliasSource(t *testing.T) {
	source := []protocol.DesktopDisplayCapability{{ID: "10", Name: "Primary", Width: 1920, Height: 1080}}
	cloned := cloneDesktopDisplays(source)
	cloned[0].Name = "Changed"
	if source[0].Name != "Primary" {
		t.Fatal("cloned display capabilities alias source storage")
	}
}
