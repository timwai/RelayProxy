package codec

import (
	"context"
	"testing"
	"time"
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

func resetNVCodecCanaryForTest() {
	nvcodecCanaryRequested.Store(false)
	nvcodecCanaryEligible.Store(false)
	nvcodecCanaryTripped.Store(false)
	nvcodecCanaryTripAt.Store(0)
	nvcodecCanaryTripMu.Lock()
	nvcodecCanaryTripReason = ""
	nvcodecCanaryTripMu.Unlock()
}

func TestNVCodecCanaryGateDefaultsDisabled(t *testing.T) {
	resetNVCodecCanaryForTest()
	t.Cleanup(resetNVCodecCanaryForTest)
	if NVCodecCanaryEnabled() {
		t.Fatal("NVCodec canary unexpectedly enabled")
	}
	backend := platformNVCodecH265444Backend()
	if backend.productionReady {
		if backend.enabled == nil {
			t.Fatal("production NVCodec backend is missing canary gate")
		}
		if backend.enabled() {
			t.Fatal("production NVCodec backend ignored disabled canary gate")
		}
		return
	}
	if backend.productionGateError() == nil {
		t.Fatal("unsupported NVCodec backend was not rejected by production gate")
	}
}

func TestNVCodecCanaryGateCanBeExplicitlyEnabled(t *testing.T) {
	resetNVCodecCanaryForTest()
	SetNVCodecCanaryEnabled(true)
	t.Cleanup(resetNVCodecCanaryForTest)
	if !NVCodecCanaryEnabled() {
		t.Fatal("NVCodec canary opt-in did not enable gate")
	}
}

func TestNVCodecCanaryCircuitBreakerDisablesActiveGate(t *testing.T) {
	resetNVCodecCanaryForTest()
	SetNVCodecCanaryEnabled(true)
	t.Cleanup(resetNVCodecCanaryForTest)

	if !NVCodecCanaryEnabled() {
		t.Fatal("canary should be active before trip")
	}
	if !TripNVCodecCanary("test runtime failure") {
		t.Fatal("first circuit trip was not recorded")
	}
	if NVCodecCanaryEnabled() || !NVCodecCanaryCircuitTripped() {
		t.Fatal("tripped canary remained active")
	}
	if TripNVCodecCanary("second failure") {
		t.Fatal("second circuit trip should be idempotent")
	}
	SetNVCodecCanaryEnabled(true)
	if NVCodecCanaryEnabled() {
		t.Fatal("config reapply bypassed process-lifetime circuit breaker")
	}
}

func TestNVCodecCanaryCircuitBreakerRecordsReasonAndTime(t *testing.T) {
	resetNVCodecCanaryForTest()
	SetNVCodecCanaryEnabled(true)
	t.Cleanup(resetNVCodecCanaryForTest)

	before := time.Now().Add(-time.Second).UnixMilli()
	if !TripNVCodecCanary("NVDEC decode failure: device lost") {
		t.Fatal("circuit trip was not recorded")
	}
	at, reason := NVCodecCanaryTripDetails()
	if at < before {
		t.Fatalf("trip time=%d before lower bound=%d", at, before)
	}
	if reason != "NVDEC decode failure: device lost" {
		t.Fatalf("trip reason=%q", reason)
	}
}

func TestNVCodecCanaryRequestedEligibleActiveStates(t *testing.T) {
	resetNVCodecCanaryForTest()
	t.Cleanup(resetNVCodecCanaryForTest)

	SetNVCodecCanaryRequested(true)
	if !NVCodecCanaryRequested() {
		t.Fatal("requested state was not retained")
	}
	if NVCodecCanaryEligible() || NVCodecCanaryEnabled() {
		t.Fatal("requested canary became active without eligibility")
	}

	SetNVCodecCanaryEligible(true)
	if !NVCodecCanaryEligible() || !NVCodecCanaryEnabled() {
		t.Fatal("requested and eligible canary did not become active")
	}

	if !TripNVCodecCanary("test trip") {
		t.Fatal("active canary did not trip")
	}
	if !NVCodecCanaryRequested() || !NVCodecCanaryEligible() {
		t.Fatal("trip erased requested or eligible state")
	}
	if NVCodecCanaryEnabled() {
		t.Fatal("tripped canary remained active")
	}
}
