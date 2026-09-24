package codec

// OneVPLProbe describes Intel oneVPL runtime capabilities that Relay Desktop can
// use for a future HEVC 4:4:4 backend. Hardware capability is intentionally
// separate from public DesktopCodecCapability advertisement: RelayProxy must
// have both an implemented encoder and decoder path before Chroma444 is exposed
// to controllers.
type OneVPLProbe struct {
	DispatcherAvailable bool
	HardwareRuntime     bool
	HEVC444Encode       bool
	HEVC444Decode       bool
	Error               string
}

func (p OneVPLProbe) HEVC444EndToEnd() bool {
	return p.DispatcherAvailable &&
		p.HardwareRuntime &&
		p.HEVC444Encode &&
		p.HEVC444Decode
}
