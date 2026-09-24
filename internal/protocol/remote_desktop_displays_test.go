package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDesktopVideoControlDisplayIDDistinguishesVirtualDesktopRequest(t *testing.T) {
	without, err := json.Marshal(DesktopVideoControl{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "displayId") {
		t.Fatalf("unset display control serialized displayId: %s", without)
	}

	virtualDesktop := ""
	with, err := json.Marshal(DesktopVideoControl{DisplayID: &virtualDesktop})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(with), `"displayId":""`) {
		t.Fatalf("virtual desktop display request lost empty displayId: %s", with)
	}
}

func TestCloneDesktopCodecCapabilitiesDeepCopiesDirectionalChroma(t *testing.T) {
	original := []DesktopCodecCapability{{
		Codec:        "h265",
		EncodeChroma: []string{"420", "444"},
		DecodeChroma: []string{"444"},
	}}
	cloned := CloneDesktopCodecCapabilities(original)
	if len(cloned) != 1 {
		t.Fatalf("cloned len=%d", len(cloned))
	}
	cloned[0].EncodeChroma[0] = "changed"
	cloned[0].DecodeChroma[0] = "changed"
	if original[0].EncodeChroma[0] != "420" || original[0].DecodeChroma[0] != "444" {
		t.Fatalf("clone aliases original slices: original=%+v cloned=%+v", original, cloned)
	}
}


func TestCloneDesktopGPUCapabilityDeepCopiesFormats(t *testing.T) {
	original := &DesktopGPUCapability{
		Backend:        "d3d11",
		DecodeZeroCopy: true,
		DisplayZeroCopy: true,
		Formats:        []string{"nv12", "ayuv"},
	}
	cloned := CloneDesktopGPUCapability(original)
	if cloned == nil {
		t.Fatal("GPU capability clone is nil")
	}
	cloned.Formats[0] = "changed"
	if original.Formats[0] != "nv12" {
		t.Fatalf("GPU capability clone aliases original: original=%+v cloned=%+v", original, cloned)
	}
}
