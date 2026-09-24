package codec

import "testing"

func TestH264ProbeCapability(t *testing.T) {
	probe := H264Probe{
		MediaFoundation:      true,
		HardwareEncoderCount: 1,
		HardwareDecoderCount: 2,
		SoftwareEncoderCount: 1,
		SoftwareDecoderCount: 1,
	}
	capability := probe.Capability()
	if capability.Codec != "h264" || capability.Encoder != "media-foundation" {
		t.Fatalf("unexpected codec capability: %+v", capability)
	}
	if !capability.Encode || !capability.Decode || !capability.Hardware || !capability.Chroma420 || !capability.BitDepth8 {
		t.Fatalf("incomplete H.264 capability: %+v", capability)
	}
	if len(capability.EncodeChroma) != 1 || capability.EncodeChroma[0] != "420" ||
		len(capability.DecodeChroma) != 1 || capability.DecodeChroma[0] != "420" {
		t.Fatalf("missing directional H.264 chroma: %+v", capability)
	}
}

func TestH264ProbeHardwareRequiresBothDirections(t *testing.T) {
	probe := H264Probe{HardwareEncoderCount: 1, SoftwareDecoderCount: 1}
	if !probe.EncodeAvailable() || !probe.DecodeAvailable() {
		t.Fatal("available software/hardware transforms were ignored")
	}
	if probe.HardwareEndToEnd() {
		t.Fatal("hardware end-to-end reported without a hardware decoder")
	}
}

func TestH264ProbeNoEncoderClearsEncoderName(t *testing.T) {
	capability := (H264Probe{SoftwareDecoderCount: 1}).Capability()
	if capability.Encode || capability.Encoder != "" || !capability.Decode {
		t.Fatalf("unexpected decoder-only capability: %+v", capability)
	}
}

func TestH265ProbeCapability(t *testing.T) {
	probe := H265Probe{
		MediaFoundation:      true,
		HardwareEncoderCount: 2,
		HardwareDecoderCount: 1,
		SoftwareEncoderCount: 0,
		SoftwareDecoderCount: 1,
	}
	capability := probe.Capability()
	if capability.Codec != "h265" || capability.Encoder != "media-foundation" {
		t.Fatalf("unexpected HEVC capability: %+v", capability)
	}
	if !capability.Encode || !capability.Decode || !capability.Hardware ||
		!capability.Chroma420 || !capability.BitDepth8 {
		t.Fatalf("incomplete H.265 capability: %+v", capability)
	}
	if len(capability.EncodeChroma) != 1 || capability.EncodeChroma[0] != "420" ||
		len(capability.DecodeChroma) != 1 || capability.DecodeChroma[0] != "420" {
		t.Fatalf("missing directional H.265 chroma: %+v", capability)
	}
}

func TestH265ProbeNoEncoderClearsEncoderName(t *testing.T) {
	capability := (H265Probe{HardwareDecoderCount: 1}).Capability()
	if capability.Encode || capability.Encoder != "" || !capability.Decode {
		t.Fatalf("unexpected H.265 decoder-only capability: %+v", capability)
	}
}

func TestH265ProbeHardwareRequiresBothDirections(t *testing.T) {
	probe := H265Probe{HardwareEncoderCount: 1, SoftwareDecoderCount: 1}
	if !probe.EncodeAvailable() || !probe.DecodeAvailable() {
		t.Fatal("available H.265 transforms were ignored")
	}
	if probe.HardwareEndToEnd() {
		t.Fatal("H.265 hardware end-to-end reported without a hardware decoder")
	}
}

func TestH265CapabilityAddsOneVPL444OnlyEndToEnd(t *testing.T) {
	capability, available := H265Capability(H265Probe{}, OneVPLProbe{
		DispatcherAvailable: true,
		HardwareRuntime:     true,
		HEVC444Encode:       true,
		HEVC444Decode:       true,
	})
	if !available {
		t.Fatal("oneVPL HEVC 4:4:4 capability was not made available")
	}
	if capability.Codec != "h265" || capability.Encoder != "onevpl-hevc444" {
		t.Fatalf("unexpected oneVPL capability: %+v", capability)
	}
	if !capability.Encode || !capability.Decode || !capability.Chroma444 ||
		capability.Chroma420 || !capability.BitDepth8 || !capability.Hardware {
		t.Fatalf("incorrect oneVPL-only capability: %+v", capability)
	}
}

func TestH265CapabilityMergesMediaFoundationAndOneVPL(t *testing.T) {
	capability, available := H265Capability(
		H265Probe{HardwareEncoderCount: 1, HardwareDecoderCount: 1},
		OneVPLProbe{
			DispatcherAvailable: true,
			HardwareRuntime:     true,
			HEVC444Encode:       true,
			HEVC444Decode:       true,
		},
	)
	if !available {
		t.Fatal("combined HEVC capability was not available")
	}
	if !capability.Chroma420 || !capability.Chroma444 {
		t.Fatalf("combined HEVC chroma capability is incomplete: %+v", capability)
	}
	if capability.Encoder != "media-foundation+onevpl-hevc444" {
		t.Fatalf("encoder=%q", capability.Encoder)
	}
}

func TestH265CapabilityDoesNotAdvertisePartialOneVPL444(t *testing.T) {
	capability, available := H265Capability(H265Probe{}, OneVPLProbe{
		DispatcherAvailable: true,
		HardwareRuntime:     true,
		HEVC444Encode:       true,
	})
	if available || capability.Chroma444 || capability.Encode || capability.Decode {
		t.Fatalf("partial oneVPL 4:4:4 was advertised: %+v", capability)
	}
	if capability.Chroma420 || capability.BitDepth8 {
		t.Fatalf("empty Media Foundation probe advertised 4:2:0: %+v", capability)
	}
}

func TestH265CapabilityPreservesMixedDirectional420WithOneVPL(t *testing.T) {
	capability, available := H265Capability(
		H265Probe{HardwareDecoderCount: 1},
		OneVPLProbe{
			DispatcherAvailable: true,
			HardwareRuntime:     true,
			HEVC444Encode:       true,
			HEVC444Decode:       true,
		},
	)
	if !available {
		t.Fatal("mixed HEVC capability was not available")
	}
	if capability.Chroma420 {
		t.Fatalf("legacy shared 4:2:0 flag should be conservative: %+v", capability)
	}
	if len(capability.EncodeChroma) != 1 || capability.EncodeChroma[0] != "444" {
		t.Fatalf("encode chroma=%v want [444]", capability.EncodeChroma)
	}
	if len(capability.DecodeChroma) != 2 ||
		capability.DecodeChroma[0] != "420" || capability.DecodeChroma[1] != "444" {
		t.Fatalf("decode chroma=%v want [420 444]", capability.DecodeChroma)
	}
}
