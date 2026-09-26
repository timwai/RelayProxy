package codec

import (
	"context"
	"testing"
)

func productionTestH265444Backend(name string) h265444Backend {
	return h265444Backend{
		name:              name,
		productionReady:   true,
		zeroCopyValidated: true,
		lifecycle:         h265444SessionLifecycleContract(),
		interop:           h265444D3D11NativeAYUVContract(),
	}
}

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
	vendorA := productionTestH265444Backend("vendor-a")
	vendorA.probe = func(context.Context) H265444BackendProbe {
		return H265444BackendProbe{
			HardwareRuntime: true,
			Encode:          true,
		}
	}
	vendorA.openEncoder = func(context.Context, VideoConfig) (SequenceHeaderEncoder, error) {
		return nil, ErrEncoderUnavailable
	}
	vendorB := productionTestH265444Backend("vendor-b")
	vendorB.probe = func(context.Context) H265444BackendProbe {
		return H265444BackendProbe{
			Backend:         "vendor-b-runtime",
			HardwareRuntime: true,
			Decode:          true,
		}
	}
	vendorB.openDecoder = func(context.Context, VideoConfig) (Decoder, error) {
		return nil, ErrDecoderUnavailable
	}
	backends := []h265444Backend{vendorA, vendorB}

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

func TestProbeH265444BackendsDoesNotAdvertiseProbeOnlyImplementation(t *testing.T) {
	backend := productionTestH265444Backend("probe-only")
	backend.productionReady = false
	backend.probe = func(context.Context) H265444BackendProbe {
		return H265444BackendProbe{
			HardwareRuntime: true,
			Encode:          true,
			Decode:          true,
		}
	}
	backend.openEncoder = func(context.Context, VideoConfig) (SequenceHeaderEncoder, error) {
		return nil, ErrEncoderUnavailable
	}
	backend.openDecoder = func(context.Context, VideoConfig) (Decoder, error) {
		return nil, ErrDecoderUnavailable
	}
	got := probeH265444Backends(context.Background(), []h265444Backend{backend})
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

func TestProbeH265444BackendsRequiresZeroCopyValidation(t *testing.T) {
	backend := productionTestH265444Backend("vendor")
	backend.zeroCopyValidated = false
	backend.probe = func(context.Context) H265444BackendProbe {
		return H265444BackendProbe{
			HardwareRuntime: true,
			Encode:          true,
			Decode:          true,
		}
	}
	backend.openEncoderD3D11 = func(context.Context, VideoConfig, uintptr) (SequenceHeaderEncoder, error) {
		return nil, ErrEncoderUnavailable
	}
	backend.openDecoderD3D11 = func(context.Context, VideoConfig, uintptr) (Decoder, error) {
		return nil, ErrDecoderUnavailable
	}

	got := probeH265444Backends(context.Background(), []h265444Backend{backend})
	if len(got) != 1 || got[0].Encode || got[0].Decode || got[0].EndToEnd() {
		t.Fatalf("unvalidated zero-copy backend was advertised: %+v", got)
	}
	if got[0].Error == "" {
		t.Fatal("zero-copy gate did not explain why the backend is disabled")
	}
}

func TestProbeH265444BackendsAllowsD3D11OnlyProductionOpeners(t *testing.T) {
	backend := productionTestH265444Backend("vendor")
	backend.probe = func(context.Context) H265444BackendProbe {
		return H265444BackendProbe{
			HardwareRuntime: true,
			Encode:          true,
			Decode:          true,
		}
	}
	backend.openEncoderD3D11 = func(context.Context, VideoConfig, uintptr) (SequenceHeaderEncoder, error) {
		return nil, ErrEncoderUnavailable
	}
	backend.openDecoderD3D11 = func(context.Context, VideoConfig, uintptr) (Decoder, error) {
		return nil, ErrDecoderUnavailable
	}

	got := probeH265444Backends(context.Background(), []h265444Backend{backend})
	if len(got) != 1 || !got[0].Encode || !got[0].Decode || !got[0].EndToEnd() {
		t.Fatalf("D3D11-only production backend was rejected: %+v", got)
	}
}

func TestNVCodecCanaryGateDefaultsDisabled(t *testing.T) {
	nvcodecCanaryRequested.Store(false)
	nvcodecCanaryTripped.Store(false)
	t.Cleanup(func() {
		nvcodecCanaryRequested.Store(false)
		nvcodecCanaryTripped.Store(false)
	})
	if NVCodecCanaryEnabled() {
		t.Fatal("NVCodec canary unexpectedly enabled")
	}
	backend := platformNVCodecH265444Backend()
	if backend.enabled == nil || backend.enabled() {
		t.Fatal("platform NVCodec backend ignored disabled canary gate")
	}
}

func TestNVCodecCanaryGateCanBeExplicitlyEnabled(t *testing.T) {
	nvcodecCanaryRequested.Store(false)
	nvcodecCanaryTripped.Store(false)
	SetNVCodecCanaryEnabled(true)
	t.Cleanup(func() {
		nvcodecCanaryRequested.Store(false)
		nvcodecCanaryTripped.Store(false)
	})
	if !NVCodecCanaryEnabled() {
		t.Fatal("NVCodec canary opt-in did not enable gate")
	}
}

func TestNVCodecCanaryCircuitBreakerDisablesActiveGate(t *testing.T) {
	nvcodecCanaryRequested.Store(false)
	nvcodecCanaryTripped.Store(false)
	SetNVCodecCanaryEnabled(true)
	t.Cleanup(func() {
		nvcodecCanaryRequested.Store(false)
		nvcodecCanaryTripped.Store(false)
	})

	if !NVCodecCanaryEnabled() {
		t.Fatal("canary should be active before trip")
	}
	if !TripNVCodecCanary() {
		t.Fatal("first circuit trip was not recorded")
	}
	if NVCodecCanaryEnabled() || !NVCodecCanaryCircuitTripped() {
		t.Fatal("tripped canary remained active")
	}
	if TripNVCodecCanary() {
		t.Fatal("second circuit trip should be idempotent")
	}
	SetNVCodecCanaryEnabled(true)
	if NVCodecCanaryEnabled() {
		t.Fatal("config reapply bypassed process-lifetime circuit breaker")
	}
}
