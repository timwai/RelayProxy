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
