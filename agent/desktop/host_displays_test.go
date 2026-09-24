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


func TestDesktopDisplayRecoveryTarget(t *testing.T) {
	displays := []protocol.DesktopDisplayCapability{
		{ID: "10", Name: "Primary", Primary: true},
		{ID: "20", Name: "Secondary"},
	}

	tests := []struct {
		name    string
		cfg     HostConfig
		displays []protocol.DesktopDisplayCapability
		want    string
		recover bool
	}{
		{
			name: "current display still available",
			cfg: HostConfig{DisplayID: "20", CaptureBackend: protocol.DesktopCaptureDXGI},
			displays: displays,
		},
		{
			name: "auto returns to virtual desktop",
			cfg: HostConfig{DisplayID: "30", CaptureBackend: protocol.DesktopCaptureAuto},
			displays: displays,
			want: "", recover: true,
		},
		{
			name: "gdi returns to virtual desktop",
			cfg: HostConfig{DisplayID: "30", CaptureBackend: protocol.DesktopCaptureGDI},
			displays: displays,
			want: "", recover: true,
		},
		{
			name: "dxgi selects primary",
			cfg: HostConfig{DisplayID: "30", CaptureBackend: protocol.DesktopCaptureDXGI},
			displays: displays,
			want: "10", recover: true,
		},
		{
			name: "wgc selects first when no primary",
			cfg: HostConfig{DisplayID: "30", CaptureBackend: protocol.DesktopCaptureWGC},
			displays: []protocol.DesktopDisplayCapability{{ID:"20"}, {ID:"10"}},
			want: "20", recover: true,
		},
		{
			name: "per display backend cannot recover with no displays",
			cfg: HostConfig{DisplayID: "30", CaptureBackend: protocol.DesktopCaptureDXGI},
			displays: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, recover := desktopDisplayRecoveryTarget(tt.cfg, tt.displays)
			if got != tt.want || recover != tt.recover {
				t.Fatalf("recovery target=(%q,%v) want=(%q,%v)", got, recover, tt.want, tt.recover)
			}
		})
	}
}
