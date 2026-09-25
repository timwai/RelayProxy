package codec

import (
	"context"
	"testing"
)

func TestH265444BackendProbeRequiresHardwareRuntimeAndBothDirections(t *testing.T) {
	probe := H265444BackendProbe{
		Backend:            "test-hevc444",
		HardwareRuntime:    true,
		Encode:             true,
		Decode:             true,
		SystemMemoryEncode: true,
		SystemMemoryDecode: true,
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
			openEncoder: func(context.Context, VideoConfig) (SequenceHeaderEncoder, error) {
				return nil, ErrEncoderUnavailable
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
			openDecoder: func(context.Context, VideoConfig) (Decoder, error) {
				return nil, ErrDecoderUnavailable
			},
		},
	}
	got := probeH265444Backends(context.Background(), backends)
	if len(got) != 2 {
		t.Fatalf("probe count=%d want=2", len(got))
	}
	if got[0].Backend != "vendor-a" || !got[0].Encode || got[0].Decode ||
		!got[0].SystemMemoryEncode || got[0].D3D11Encode {
		t.Fatalf("first probe=%+v", got[0])
	}
	if got[1].Backend != "vendor-b-runtime" || got[1].Encode || !got[1].Decode ||
		!got[1].SystemMemoryDecode || got[1].D3D11Decode {
		t.Fatalf("second probe=%+v", got[1])
	}
}

func TestProbeH265444BackendsTracksD3D11OnlyReadiness(t *testing.T) {
	backends := []h265444Backend{{
		name: "gpu-only",
		probe: func(context.Context) H265444BackendProbe {
			return H265444BackendProbe{
				HardwareRuntime: true,
				Encode:          true,
				Decode:          true,
			}
		},
		openEncoderD3D11: func(context.Context, VideoConfig, uintptr) (SequenceHeaderEncoder, error) {
			return nil, ErrEncoderUnavailable
		},
		openDecoderD3D11: func(context.Context, VideoConfig, uintptr) (Decoder, error) {
			return nil, ErrDecoderUnavailable
		},
		probeDecoderD3D11: func(context.Context, VideoConfig, uintptr) error {
			return ErrDecoderUnavailable
		},
	}}
	got := probeH265444Backends(context.Background(), backends)
	if len(got) != 1 {
		t.Fatalf("probe count=%d want=1", len(got))
	}
	probe := got[0]
	if !probe.Encode || !probe.Decode ||
		probe.SystemMemoryEncode || probe.SystemMemoryDecode ||
		!probe.D3D11Encode || !probe.D3D11Decode ||
		!probe.D3D11EndToEnd() || !probe.EndToEnd() {
		t.Fatalf("D3D11-only readiness=%+v", probe)
	}
	if H265444SystemMemoryEndToEndAvailable(got) {
		t.Fatalf("D3D11-only backend leaked into system-memory readiness: %+v", got)
	}
	if !H265444D3D11EndToEndAvailable(got) {
		t.Fatalf("D3D11-only backend was not available to GPU validation: %+v", got)
	}
}

func TestProbeH265444BackendsDoesNotAdvertiseProbeOnlyImplementation(t *testing.T) {
	got := probeH265444Backends(context.Background(), []h265444Backend{{
		name: "probe-only",
		probe: func(context.Context) H265444BackendProbe {
			return H265444BackendProbe{
				HardwareRuntime: true,
				Encode:          true,
				Decode:          true,
			}
		},
	}})
	if len(got) != 1 || got[0].Encode || got[0].Decode || got[0].EndToEnd() {
		t.Fatalf("probe-only backend was advertised: %+v", got)
	}
}

func TestProbeH265444BackendsReportsMissingProbe(t *testing.T) {
	got := probeH265444Backends(context.Background(), []h265444Backend{{name: "missing"}})
	if len(got) != 1 || got[0].Backend != "missing" || got[0].Error == "" {
		t.Fatalf("missing probe result=%+v", got)
	}
}
