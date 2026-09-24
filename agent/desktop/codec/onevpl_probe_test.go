package codec

import "testing"

func TestOneVPLProbeRequiresBothDirectionsForEndToEnd444(t *testing.T) {
	probe := OneVPLProbe{
		DispatcherAvailable: true,
		HardwareRuntime:     true,
		HEVC444Encode:       true,
		HEVC444Decode:       true,
	}
	if !probe.HEVC444EndToEnd() {
		t.Fatal("complete oneVPL HEVC 4:4:4 capability was rejected")
	}
	probe.HEVC444Decode = false
	if probe.HEVC444EndToEnd() {
		t.Fatal("encode-only oneVPL capability was reported as end-to-end")
	}
}

func TestOneVPLProbeDoesNotTreatDispatcherAsCodecSupport(t *testing.T) {
	probe := OneVPLProbe{DispatcherAvailable: true}
	if probe.HEVC444EndToEnd() {
		t.Fatal("dispatcher presence alone was reported as 4:4:4 support")
	}
}
