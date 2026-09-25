package protocol

import "testing"

func TestCloneDesktopCapabilitiesDeepCopiesGPUDetails(t *testing.T) {
	input := DesktopCapabilities{
		Captures: []DesktopCaptureCapability{{Backend: "dxgi"}},
		Codecs: []DesktopCodecCapability{{
			Codec: "h265", EncodeChroma: []string{"420", "444"}, DecodeChroma: []string{"420", "444"},
		}},
		GPU: &DesktopGPUCapability{Backend: "d3d11", Formats: []string{"nv12", "ayuv"}},
		GPUCandidates: []DesktopGPUCandidateDiagnostics{{
			Backend: "nvcodec-hevc444", Vendor: "nvidia", DeviceProbe: true, HEVC444Decode: true,
		}},
		Displays:    []DesktopDisplayCapability{{ID: "display-1", Width: 1920, Height: 1080}},
		AudioCodecs: []string{DesktopAudioCodecOpus},
	}
	got := CloneDesktopCapabilities(input)

	got.Captures[0].Backend = "mutated"
	got.Codecs[0].EncodeChroma[0] = "mutated"
	got.GPU.Formats[0] = "mutated"
	got.GPUCandidates[0].Backend = "mutated"
	got.Displays[0].ID = "mutated"
	got.AudioCodecs[0] = "mutated"

	if input.Captures[0].Backend != "dxgi" ||
		input.Codecs[0].EncodeChroma[0] != "420" ||
		input.GPU.Formats[0] != "nv12" ||
		input.GPUCandidates[0].Backend != "nvcodec-hevc444" ||
		input.Displays[0].ID != "display-1" ||
		input.AudioCodecs[0] != DesktopAudioCodecOpus {
		t.Fatalf("clone aliases source capability: source=%+v clone=%+v", input, got)
	}
}

func TestCloneRemoteDesktopTargetsDeepCopiesCapabilities(t *testing.T) {
	input := []RemoteDesktopTarget{{
		DeviceID: "target-a",
		Capabilities: DesktopCapabilities{
			GPUCandidates: []DesktopGPUCandidateDiagnostics{{
				Backend: "amf-hevc444", Vendor: "amd", RuntimeAvailable: true,
			}},
		},
	}}
	got := CloneRemoteDesktopTargets(input)
	got[0].Capabilities.GPUCandidates[0].Vendor = "mutated"
	if input[0].Capabilities.GPUCandidates[0].Vendor != "amd" {
		t.Fatalf("target clone aliases source: source=%+v clone=%+v", input, got)
	}
}
