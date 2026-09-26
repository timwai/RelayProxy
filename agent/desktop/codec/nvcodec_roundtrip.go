package codec

type NVCodecValidationIdentity struct {
	Adapter              string `json:"adapter,omitempty"`
	AdapterVendorID      uint32 `json:"adapterVendorId,omitempty"`
	AdapterDeviceID      uint32 `json:"adapterDeviceId,omitempty"`
	AdapterSubSysID      uint32 `json:"adapterSubSysId,omitempty"`
	AdapterRevision      uint32 `json:"adapterRevision,omitempty"`
	AdapterDriverVersion uint64 `json:"adapterDriverVersion,omitempty"`
}

func (i NVCodecValidationIdentity) MatchesReport(report NVCodecH265444RoundTripReport) bool {
	return i.AdapterVendorID != 0 &&
		i.AdapterVendorID == report.AdapterVendorID &&
		i.AdapterDeviceID == report.AdapterDeviceID &&
		i.AdapterSubSysID == report.AdapterSubSysID &&
		i.AdapterRevision == report.AdapterRevision &&
		i.AdapterDriverVersion != 0 &&
		i.AdapterDriverVersion == report.AdapterDriverVersion
}

// NVCodecH265444RoundTripReport describes an explicit NVIDIA Windows self-test.
// It is intentionally separate from normal backend probing: callers must opt in
// because the test creates GPU resources and performs real encode/decode work.
type NVCodecH265444RoundTripReport struct {
	Passed                      bool   `json:"passed"`
	Backend                     string `json:"backend"`
	Adapter                     string `json:"adapter,omitempty"`
	AdapterVendorID             uint32 `json:"adapterVendorId,omitempty"`
	AdapterDeviceID             uint32 `json:"adapterDeviceId,omitempty"`
	AdapterSubSysID             uint32 `json:"adapterSubSysId,omitempty"`
	AdapterRevision             uint32 `json:"adapterRevision,omitempty"`
	AdapterDriverVersion        uint64 `json:"adapterDriverVersion,omitempty"`
	ValidatedAtUnixMs           int64  `json:"validatedAtUnixMs,omitempty"`
	Width                       int    `json:"width"`
	Height                      int    `json:"height"`
	FPS                         int    `json:"fps"`
	InitialBitrate              int    `json:"initialBitrate"`
	ReconfiguredBitrate         int    `json:"reconfiguredBitrate"`
	D3D11DeviceCreated          bool   `json:"d3d11DeviceCreated"`
	SourceTextureCreated        bool   `json:"sourceTextureCreated"`
	EncoderOpened               bool   `json:"encoderOpened"`
	SequenceHeaderBytes         int    `json:"sequenceHeaderBytes"`
	EncodedFrames               int    `json:"encodedFrames"`
	FirstPacketBytes            int    `json:"firstPacketBytes"`
	FirstPacketKeyFrame         bool   `json:"firstPacketKeyFrame"`
	DecoderOpened               bool   `json:"decoderOpened"`
	DecodedFrames               int    `json:"decodedFrames"`
	D3D11OutputValidated        bool   `json:"d3d11OutputValidated"`
	ReadbackValidated           bool   `json:"readbackValidated"`
	SamplesChecked              int    `json:"samplesChecked"`
	MaxChannelError             int    `json:"maxChannelError"`
	BitrateReconfigureValidated bool   `json:"bitrateReconfigureValidated"`
	ReconfigurePacketKeyFrame   bool   `json:"reconfigurePacketKeyFrame"`
	ForceIDRValidated           bool   `json:"forceIdrValidated"`
	ForceIDRPacketKeyFrame      bool   `json:"forceIdrPacketKeyFrame"`
	CleanupValidated            bool   `json:"cleanupValidated"`
	DurationMs                  int64  `json:"durationMs"`
	Error                       string `json:"error,omitempty"`
}
