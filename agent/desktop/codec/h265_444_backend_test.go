package codec

import (
	"context"
	"testing"
)

func TestH265444BackendProbeRequiresHardwareRuntimeAndBothDirections(t *testing.T) {
	probe := H265444BackendProbe{
		Backend:         "test-hevc444",
		HardwareRuntime: true,
		Encode:          true,
		Decode:          true,
	}
	if !probe.EndToEnd() {
		t.Fatal("complete HEVC 4:4:4 backend probe was rejected")
	}
	probe.HardwareRuntime = false
	if probe.EndToEnd() {
		t.Fatal("backend without a hardware runtime was reported as end-to-end")
	}
}

func TestProbeH265444BackendsPreservesPriorityAndBackfillsName(t *testing.T) {
	backends := []h265444Backend{
		{
			name: "vendor-a",
			probe: func(context.Context) H265444BackendProbe {
				return H265444BackendProbe{
					HardwareRuntime: true,
					Encode:          true,
				}
			},
		},
		{
			name: "vendor-b",
			probe: func(context.Context) H265444BackendProbe {
				return H265444BackendProbe{
					Backend:         "vendor-b-runtime",
					HardwareRuntime: true,
					Decode:          true,
				}
			},
		},
	}
	got := probeH265444Backends(context.Background(), backends)
	if len(got) != 2 {
		t.Fatalf("probe count=%d want=2", len(got))
	}
	if got[0].Backend != "vendor-a" || !got[0].Encode || got[0].Decode {
		t.Fatalf("first probe=%+v", got[0])
	}
	if got[1].Backend != "vendor-b-runtime" || got[1].Encode || !got[1].Decode {
		t.Fatalf("second probe=%+v", got[1])
	}
}

func TestProbeH265444BackendsReportsMissingProbe(t *testing.T) {
	got := probeH265444Backends(context.Background(), []h265444Backend{{name: "missing"}})
	if len(got) != 1 || got[0].Backend != "missing" || got[0].Error == "" {
		t.Fatalf("missing probe result=%+v", got)
	}
}
