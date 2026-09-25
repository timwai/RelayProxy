package codec

import "context"

// H265444RuntimeCandidate describes a vendor runtime that may host a future
// HEVC 8-bit 4:4:4 backend. Candidate probes are diagnostics only: they are
// intentionally separate from h265444BackendRegistry and therefore cannot make
// Chroma444 public by themselves.
type H265444RuntimeCandidate struct {
	Backend          string
	Vendor           string
	RuntimeAvailable bool
	// EncodeRuntime / DecodeRuntime mean that the vendor runtime entry points
	// needed to attempt those integrations are loadable. They are not evidence
	// that this GPU supports HEVC 4:4:4 in either direction.
	EncodeRuntime bool
	DecodeRuntime bool
	// DeviceProbe means a real vendor device/context capability query completed.
	// HEVC444Encode / HEVC444Decode are still diagnostics-only until RelayProxy
	// has corresponding production openers and zero-copy validation.
	DeviceProbe           bool
	DeviceCount           int
	EncodeCapabilityKnown bool
	DecodeCapabilityKnown bool
	HEVC444Encode         bool
	HEVC444Decode         bool
	Version               string
	Implemented           bool
	Limitation            string
	Error                 string
}

// ProbeH265444RuntimeCandidates returns diagnostic-only vendor runtime probes.
// A result here must never be used as a substitute for an implemented backend
// probe in H265CapabilityWith444Backends.
func ProbeH265444RuntimeCandidates(ctx context.Context) []H265444RuntimeCandidate {
	return probePlatformH265444RuntimeCandidates(ctx)
}
