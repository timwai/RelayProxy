package codec

import "testing"

func TestD3D11AYUVGPUCapabilityRequiresFullRuntimePath(t *testing.T) {
	oneVPL := OneVPLProbe{
		DispatcherAvailable: true,
		HardwareRuntime:     true,
		HEVC444D3D11Encode:  true,
		HEVC444D3D11Decode:  true,
	}
	gpu := D3D11AYUVProbe{
		DeviceAvailable: true,
		BGRAInput:       true,
		AYUVOutput:      true,
		AYUVInput:       true,
		BGRAOutput:      true,
		OneVPLEncode:    true,
		OneVPLDecode:    true,
	}
	capability := D3D11AYUVGPUCapability(oneVPL, gpu)
	if capability == nil {
		t.Fatal("complete D3D11 AYUV runtime path was not advertised")
	}
	if capability.Backend != "d3d11" ||
		!capability.EncodeZeroCopy ||
		!capability.DecodeZeroCopy ||
		!capability.DisplayZeroCopy {
		t.Fatalf("unexpected GPU capability: %+v", capability)
	}
	if len(capability.Formats) != 1 || capability.Formats[0] != "ayuv" {
		t.Fatalf("GPU formats=%v", capability.Formats)
	}

	gpu.AYUVInput = false
	if got := D3D11AYUVGPUCapability(oneVPL, gpu); got != nil {
		t.Fatalf("partial GPU path was advertised: %+v", got)
	}
}

func TestD3D11AYUVGPUCapabilityRequiresOneVPLDX11Memory(t *testing.T) {
	oneVPL := OneVPLProbe{
		DispatcherAvailable: true,
		HardwareRuntime:     true,
		HEVC444SystemEncode: true,
		HEVC444SystemDecode: true,
		HEVC444Encode:       true,
		HEVC444Decode:       true,
	}
	gpu := D3D11AYUVProbe{
		DeviceAvailable: true,
		BGRAInput:       true,
		AYUVOutput:      true,
		AYUVInput:       true,
		BGRAOutput:      true,
		OneVPLEncode:    true,
		OneVPLDecode:    true,
	}
	if got := D3D11AYUVGPUCapability(oneVPL, gpu); got != nil {
		t.Fatalf("system-memory-only oneVPL path was advertised as D3D11: %+v", got)
	}
}
