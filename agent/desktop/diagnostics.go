package desktop

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	desktopadapt "relayproxy/agent/desktop/adapt"
	"relayproxy/internal/protocol"
)

const (
	desktopDiagnosticsSchemaVersion = 7
	desktopDiagnosticsMaxSamples    = 1200
	desktopDiagnosticsIntervalMs    = 500
)

type DesktopDiagnosticAdaptation struct {
	Changed               bool   `json:"changed,omitempty"`
	Reason                string `json:"reason,omitempty"`
	TargetBitrate         int    `json:"targetBitrate,omitempty"`
	TargetFPS             int    `json:"targetFps,omitempty"`
	TargetResolutionScale int    `json:"targetResolutionScale,omitempty"`
	ResolutionChanged     bool   `json:"resolutionChanged,omitempty"`
}

type DesktopDiagnosticSample struct {
	AtUnixMs   int64                        `json:"atUnixMs"`
	Config     protocol.DesktopVideoConfig  `json:"config"`
	Stats      protocol.DesktopSessionStats `json:"stats"`
	Audio      DesktopAudioDiagnostics      `json:"audio"`
	Adaptation DesktopDiagnosticAdaptation  `json:"adaptation"`
}

type DesktopDiagnosticMetricSummary struct {
	Samples int     `json:"samples"`
	Min     float64 `json:"min,omitempty"`
	Avg     float64 `json:"avg,omitempty"`
	P50     float64 `json:"p50,omitempty"`
	P95     float64 `json:"p95,omitempty"`
	Max     float64 `json:"max,omitempty"`
}

type DesktopDiagnosticsSummary struct {
	SampleCount             int                            `json:"sampleCount"`
	AudioConfiguredSamples  int                            `json:"audioConfiguredSamples,omitempty"`
	AudioReceivedFrames     uint64                         `json:"audioReceivedFrames,omitempty"`
	AudioConsumedFrames     uint64                         `json:"audioConsumedFrames,omitempty"`
	AudioQueueDroppedFrames uint64                         `json:"audioQueueDroppedFrames,omitempty"`
	AudioGenerationDiscards uint64                         `json:"audioGenerationDiscards,omitempty"`
	AudioRejectedFrames     uint64                         `json:"audioRejectedFrames,omitempty"`
	AudioReorderedFrames    uint64                         `json:"audioReorderedFrames,omitempty"`
	AudioDuplicateFrames    uint64                         `json:"audioDuplicateFrames,omitempty"`
	AudioLateFrames         uint64                         `json:"audioLateFrames,omitempty"`
	AudioPlayoutTimeouts    uint64                         `json:"audioPlayoutTimeouts,omitempty"`
	AudioConcealmentFrames  uint64                         `json:"audioConcealmentFrames,omitempty"`
	AudioGapSkippedFrames   uint64                         `json:"audioGapSkippedFrames,omitempty"`
	AudioMaxQueueFrames     int                            `json:"audioMaxQueueFrames,omitempty"`
	AudioCodecs             map[string]int                 `json:"audioCodecs,omitempty"`
	AudioQueueFrames        DesktopDiagnosticMetricSummary `json:"audioQueueFrames"`
	SessionDurationMs       int64                          `json:"sessionDurationMs"`
	SampleSpanMs            int64                          `json:"sampleSpanMs"`
	PathSwitches            int                            `json:"pathSwitches"`
	GenerationChanges       int                            `json:"generationChanges"`
	ABRChanges              int                            `json:"abrChanges"`
	ResolutionChanges       int                            `json:"resolutionChanges"`
	DroppedFrames           uint64                         `json:"droppedFrames"`
	HardwareEncodingSamples int                            `json:"hardwareEncodingSamples"`
	HardwareDecodingSamples int                            `json:"hardwareDecodingSamples"`
	Paths                   map[string]int                 `json:"paths,omitempty"`
	Codecs                  map[string]int                 `json:"codecs,omitempty"`
	Resolutions             map[string]int                 `json:"resolutions,omitempty"`
	ABRReasons              map[string]int                 `json:"abrReasons,omitempty"`
	CaptureBackends         map[string]int                 `json:"captureBackends,omitempty"`
	CaptureFormats          map[string]int                 `json:"captureFormats,omitempty"`
	EncoderBackends         map[string]int                 `json:"encoderBackends,omitempty"`
	DecoderBackends         map[string]int                 `json:"decoderBackends,omitempty"`
	RenderBackends          map[string]int                 `json:"renderBackends,omitempty"`
	RTTMs                   DesktopDiagnosticMetricSummary `json:"rttMs"`
	JitterMs                DesktopDiagnosticMetricSummary `json:"jitterMs"`
	LossPercent             DesktopDiagnosticMetricSummary `json:"lossPercent"`
	SendQueueDelayMs        DesktopDiagnosticMetricSummary `json:"sendQueueDelayMs"`
	ActualBitrate           DesktopDiagnosticMetricSummary `json:"actualBitrate"`
	ReceiveFPS              DesktopDiagnosticMetricSummary `json:"receiveFps"`
	DecodeFPS               DesktopDiagnosticMetricSummary `json:"decodeFps"`
	RenderFPS               DesktopDiagnosticMetricSummary `json:"renderFps"`
	CaptureMs               DesktopDiagnosticMetricSummary `json:"captureMs"`
	EncodeMs                DesktopDiagnosticMetricSummary `json:"encodeMs"`
	DecodeMs                DesktopDiagnosticMetricSummary `json:"decodeMs"`
	RenderMs                DesktopDiagnosticMetricSummary `json:"renderMs"`
}

type DesktopAudioValidationSummary struct {
	Requested                   bool    `json:"requested"`
	RequestedCodec              string  `json:"requestedCodec,omitempty"`
	Active                      bool    `json:"active"`
	Codec                       string  `json:"codec,omitempty"`
	ConfiguredSamples           int     `json:"configuredSamples,omitempty"`
	OpusSamples                 int     `json:"opusSamples,omitempty"`
	PCMSamples                  int     `json:"pcmSamples,omitempty"`
	PCMFallbackSamples          int     `json:"pcmFallbackSamples,omitempty"`
	SampleRate                  int     `json:"sampleRate,omitempty"`
	Channels                    int     `json:"channels,omitempty"`
	BitsPerSample               int     `json:"bitsPerSample,omitempty"`
	FrameDurationMs             int     `json:"frameDurationMs,omitempty"`
	TargetBitrate               int     `json:"targetBitrate,omitempty"`
	RawPCMBitrate               int     `json:"rawPcmBitrate,omitempty"`
	ObservedPayloadBitrate      int64   `json:"observedPayloadBitrate,omitempty"`
	ObservedCompressionRatio    float64 `json:"observedCompressionRatio,omitempty"`
	EstimatedNetworkLossPercent float64 `json:"estimatedNetworkLossPercent,omitempty"`
	ReceivedFrames              uint64  `json:"receivedFrames,omitempty"`
	ConsumedFrames              uint64  `json:"consumedFrames,omitempty"`
	ReceivedBytes               uint64  `json:"receivedBytes,omitempty"`
	ConsumedBytes               uint64  `json:"consumedBytes,omitempty"`
	QueueDroppedFrames          uint64  `json:"queueDroppedFrames,omitempty"`
	ConcealmentFrames           uint64  `json:"concealmentFrames,omitempty"`
	GapSkippedFrames            uint64  `json:"gapSkippedFrames,omitempty"`
	ReorderedFrames             uint64  `json:"reorderedFrames,omitempty"`
	DuplicateFrames             uint64  `json:"duplicateFrames,omitempty"`
	LateFrames                  uint64  `json:"lateFrames,omitempty"`
	PlayoutTimeoutFrames        uint64  `json:"playoutTimeoutFrames,omitempty"`
	MaxQueueFrames              int     `json:"maxQueueFrames,omitempty"`
	SampleSpanMs                int64   `json:"sampleSpanMs,omitempty"`
}

type DesktopHEVCValidationSummary struct {
	Requested               bool           `json:"requested"`
	HEVCSamples             int            `json:"hevcSamples"`
	H264FallbackSamples     int            `json:"h264FallbackSamples,omitempty"`
	JPEGFallbackSamples     int            `json:"jpegFallbackSamples,omitempty"`
	HardwareEncodingSamples int            `json:"hardwareEncodingSamples,omitempty"`
	HardwareDecodingSamples int            `json:"hardwareDecodingSamples,omitempty"`
	CaptureBackends         map[string]int `json:"captureBackends,omitempty"`
	CaptureFormats          map[string]int `json:"captureFormats,omitempty"`
	EncoderBackends         map[string]int `json:"encoderBackends,omitempty"`
	DecoderBackends         map[string]int `json:"decoderBackends,omitempty"`
	RenderBackends          map[string]int `json:"renderBackends,omitempty"`
}

type DesktopGPUValidationSummary struct {
	ExpectedFormat               string         `json:"expectedFormat,omitempty"`
	TargetAdvertised             bool           `json:"targetAdvertised"`
	MatchingSamples              int            `json:"matchingSamples"`
	HostEncodeZeroCopySamples    int            `json:"hostEncodeZeroCopySamples,omitempty"`
	ViewerDecodeZeroCopySamples  int            `json:"viewerDecodeZeroCopySamples,omitempty"`
	ViewerDisplayZeroCopySamples int            `json:"viewerDisplayZeroCopySamples,omitempty"`
	EndToEndZeroCopySamples      int            `json:"endToEndZeroCopySamples,omitempty"`
	FallbackSamples              int            `json:"fallbackSamples,omitempty"`
	CaptureFormats               map[string]int `json:"captureFormats,omitempty"`
	EncoderBackends              map[string]int `json:"encoderBackends,omitempty"`
	DecoderBackends              map[string]int `json:"decoderBackends,omitempty"`
	RenderBackends               map[string]int `json:"renderBackends,omitempty"`
}

type DesktopDiagnosticsReport struct {
	SchemaVersion     int                                  `json:"schemaVersion"`
	TargetID          string                               `json:"targetId,omitempty"`
	Scene             protocol.DesktopScene                `json:"scene,omitempty"`
	StartedAtUnixMs   int64                                `json:"startedAtUnixMs"`
	GeneratedAtUnixMs int64                                `json:"generatedAtUnixMs"`
	SampleIntervalMs  int                                  `json:"sampleIntervalMs"`
	Options           protocol.RemoteDesktopConnectOptions `json:"options"`
	CurrentConfig     protocol.DesktopVideoConfig          `json:"currentConfig"`
	CurrentStats      protocol.DesktopSessionStats         `json:"currentStats"`
	CurrentAudio      DesktopAudioDiagnostics              `json:"currentAudio"`
	Summary           DesktopDiagnosticsSummary            `json:"summary"`
	AudioValidation   *DesktopAudioValidationSummary       `json:"audioValidation,omitempty"`
	HEVCValidation    *DesktopHEVCValidationSummary        `json:"hevcValidation,omitempty"`
	TargetGPU           *protocol.DesktopGPUCapability             `json:"targetGpu,omitempty"`
	TargetGPUCandidates []protocol.DesktopGPUCandidateDiagnostics   `json:"targetGpuCandidates,omitempty"`
	GPUValidation       *DesktopGPUValidationSummary                 `json:"gpuValidation,omitempty"`
	Samples           []DesktopDiagnosticSample            `json:"samples"`
}

type sessionDiagnosticsRecorder struct {
	mu        sync.Mutex
	targetID  string
	options   protocol.RemoteDesktopConnectOptions
	started   time.Time
	targetGPU           *protocol.DesktopGPUCapability
	targetGPUCandidates []protocol.DesktopGPUCandidateDiagnostics
	samples             []DesktopDiagnosticSample
}

func newSessionDiagnosticsRecorder(
	targetID string,
	options protocol.RemoteDesktopConnectOptions,
	now time.Time,
) *sessionDiagnosticsRecorder {
	if now.IsZero() {
		now = time.Now()
	}
	return &sessionDiagnosticsRecorder{
		targetID: targetID,
		options:  options,
		started:  now,
		samples:  make([]DesktopDiagnosticSample, 0, desktopDiagnosticsMaxSamples),
	}
}

func (r *sessionDiagnosticsRecorder) SetTargetGPUCapability(capability *protocol.DesktopGPUCapability) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.targetGPU = protocol.CloneDesktopGPUCapability(capability)
	r.mu.Unlock()
}

func (r *sessionDiagnosticsRecorder) SetTargetGPUCandidates(
	candidates []protocol.DesktopGPUCandidateDiagnostics,
) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.targetGPUCandidates = protocol.CloneDesktopGPUCandidateDiagnostics(candidates)
	r.mu.Unlock()
}

func diagnosticAdaptation(decision desktopadapt.MediaDecision) DesktopDiagnosticAdaptation {
	return DesktopDiagnosticAdaptation{
		Changed:               decision.Changed,
		Reason:                decision.Reason,
		TargetBitrate:         decision.TargetBitrate,
		TargetFPS:             decision.TargetFPS,
		TargetResolutionScale: decision.TargetResolutionScale,
		ResolutionChanged:     decision.ResolutionChanged,
	}
}

func (r *sessionDiagnosticsRecorder) Record(
	now time.Time,
	config protocol.DesktopVideoConfig,
	stats protocol.DesktopSessionStats,
	audio DesktopAudioDiagnostics,
	decision desktopadapt.MediaDecision,
) {
	if r == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	sample := DesktopDiagnosticSample{
		AtUnixMs:   now.UnixMilli(),
		Config:     config,
		Stats:      stats,
		Audio:      audio,
		Adaptation: diagnosticAdaptation(decision),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) < desktopDiagnosticsMaxSamples {
		r.samples = append(r.samples, sample)
		return
	}
	copy(r.samples, r.samples[1:])
	r.samples[len(r.samples)-1] = sample
}

func desktopDiagnosticMetric(
	samples []DesktopDiagnosticSample,
	value func(DesktopDiagnosticSample) float64,
	valid func(DesktopDiagnosticSample, float64) bool,
) DesktopDiagnosticMetricSummary {
	values := make([]float64, 0, len(samples))
	sum := 0.0
	for _, sample := range samples {
		v := value(sample)
		if valid != nil && !valid(sample, v) {
			continue
		}
		values = append(values, v)
		sum += v
	}
	if len(values) == 0 {
		return DesktopDiagnosticMetricSummary{}
	}
	sort.Float64s(values)
	percentile := func(q float64) float64 {
		index := int(math.Ceil(q*float64(len(values)))) - 1
		if index < 0 {
			index = 0
		}
		if index >= len(values) {
			index = len(values) - 1
		}
		return values[index]
	}
	return DesktopDiagnosticMetricSummary{
		Samples: len(values),
		Min:     values[0],
		Avg:     sum / float64(len(values)),
		P50:     percentile(0.50),
		P95:     percentile(0.95),
		Max:     values[len(values)-1],
	}
}

func incrementDiagnosticCount(counts map[string]int, key string) {
	if key == "" {
		return
	}
	counts[key]++
}

func summarizeDesktopDiagnostics(
	started time.Time,
	now time.Time,
	samples []DesktopDiagnosticSample,
) DesktopDiagnosticsSummary {
	summary := DesktopDiagnosticsSummary{
		SampleCount:       len(samples),
		SessionDurationMs: now.Sub(started).Milliseconds(),
		Paths:             make(map[string]int),
		Codecs:            make(map[string]int),
		Resolutions:       make(map[string]int),
		ABRReasons:        make(map[string]int),
		AudioCodecs:       make(map[string]int),
		CaptureBackends:   make(map[string]int),
		CaptureFormats:    make(map[string]int),
		EncoderBackends:   make(map[string]int),
		DecoderBackends:   make(map[string]int),
		RenderBackends:    make(map[string]int),
	}
	if summary.SessionDurationMs < 0 {
		summary.SessionDurationMs = 0
	}
	if len(samples) > 1 {
		summary.SampleSpanMs = samples[len(samples)-1].AtUnixMs - samples[0].AtUnixMs
		if summary.SampleSpanMs < 0 {
			summary.SampleSpanMs = 0
		}
	}

	var previousPath string
	var previousGeneration uint32
	for _, sample := range samples {
		path := sample.Stats.Path
		if previousPath != "" && path != "" && path != previousPath {
			summary.PathSwitches++
		}
		if path != "" {
			previousPath = path
		}
		if previousGeneration != 0 && sample.Config.Generation != 0 &&
			sample.Config.Generation != previousGeneration {
			summary.GenerationChanges++
		}
		if sample.Config.Generation != 0 {
			previousGeneration = sample.Config.Generation
		}

		if sample.Adaptation.Changed {
			summary.ABRChanges++
			incrementDiagnosticCount(summary.ABRReasons, sample.Adaptation.Reason)
		}
		if sample.Adaptation.ResolutionChanged {
			summary.ResolutionChanges++
		}
		summary.DroppedFrames += sample.Stats.DroppedFrames
		if sample.Stats.EncoderHardware {
			summary.HardwareEncodingSamples++
		}
		if sample.Stats.DecoderHardware {
			summary.HardwareDecodingSamples++
		}

		incrementDiagnosticCount(summary.Paths, path)
		incrementDiagnosticCount(summary.Codecs, sample.Config.Codec)
		if sample.Audio.Config.Generation != 0 {
			summary.AudioConfiguredSamples++
			incrementDiagnosticCount(summary.AudioCodecs, sample.Audio.Config.Codec)
		}
		if sample.Audio.QueueFrames > summary.AudioMaxQueueFrames {
			summary.AudioMaxQueueFrames = sample.Audio.QueueFrames
		}
		if sample.Audio.ReceivedFrames > summary.AudioReceivedFrames {
			summary.AudioReceivedFrames = sample.Audio.ReceivedFrames
		}
		if sample.Audio.ConsumedFrames > summary.AudioConsumedFrames {
			summary.AudioConsumedFrames = sample.Audio.ConsumedFrames
		}
		if sample.Audio.QueueDroppedFrames > summary.AudioQueueDroppedFrames {
			summary.AudioQueueDroppedFrames = sample.Audio.QueueDroppedFrames
		}
		if sample.Audio.GenerationDiscardedFrames > summary.AudioGenerationDiscards {
			summary.AudioGenerationDiscards = sample.Audio.GenerationDiscardedFrames
		}
		if sample.Audio.RejectedFrames > summary.AudioRejectedFrames {
			summary.AudioRejectedFrames = sample.Audio.RejectedFrames
		}
		if sample.Audio.ReorderedFrames > summary.AudioReorderedFrames {
			summary.AudioReorderedFrames = sample.Audio.ReorderedFrames
		}
		if sample.Audio.DuplicateFrames > summary.AudioDuplicateFrames {
			summary.AudioDuplicateFrames = sample.Audio.DuplicateFrames
		}
		if sample.Audio.LateFrames > summary.AudioLateFrames {
			summary.AudioLateFrames = sample.Audio.LateFrames
		}
		if sample.Audio.PlayoutTimeoutFrames > summary.AudioPlayoutTimeouts {
			summary.AudioPlayoutTimeouts = sample.Audio.PlayoutTimeoutFrames
		}
		if sample.Audio.ConcealmentFrames > summary.AudioConcealmentFrames {
			summary.AudioConcealmentFrames = sample.Audio.ConcealmentFrames
		}
		if sample.Audio.GapSkippedFrames > summary.AudioGapSkippedFrames {
			summary.AudioGapSkippedFrames = sample.Audio.GapSkippedFrames
		}
		if sample.Config.Width > 0 && sample.Config.Height > 0 {
			incrementDiagnosticCount(summary.Resolutions,
				fmt.Sprintf("%dx%d", sample.Config.Width, sample.Config.Height))
		}
		incrementDiagnosticCount(summary.CaptureBackends, sample.Stats.CaptureBackend)
		incrementDiagnosticCount(summary.CaptureFormats, sample.Stats.CaptureFormat)
		incrementDiagnosticCount(summary.EncoderBackends, sample.Stats.EncoderBackend)
		incrementDiagnosticCount(summary.DecoderBackends, sample.Stats.DecoderBackend)
		incrementDiagnosticCount(summary.RenderBackends, sample.Stats.RenderBackend)
	}

	always := func(DesktopDiagnosticSample, float64) bool { return true }
	positive := func(_ DesktopDiagnosticSample, value float64) bool { return value > 0 }
	withRTT := func(sample DesktopDiagnosticSample, _ float64) bool { return sample.Stats.RTTMs > 0 }

	summary.RTTMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.RTTMs }, positive)
	summary.JitterMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.JitterMs }, withRTT)
	summary.LossPercent = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.LossPercent }, always)
	summary.SendQueueDelayMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.SendQueueDelayMs }, always)
	summary.ActualBitrate = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return float64(sample.Stats.ActualBitrate) }, always)
	summary.ReceiveFPS = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.ReceiveFPS }, always)
	summary.DecodeFPS = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.DecodeFPS }, positive)
	summary.RenderFPS = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.RenderFPS }, positive)
	summary.CaptureMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.CaptureMs }, positive)
	summary.EncodeMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.EncodeMs }, positive)
	summary.DecodeMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.DecodeMs }, positive)
	summary.RenderMs = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return sample.Stats.RenderMs }, positive)
	summary.AudioQueueFrames = desktopDiagnosticMetric(samples,
		func(sample DesktopDiagnosticSample) float64 { return float64(sample.Audio.QueueFrames) },
		func(sample DesktopDiagnosticSample, _ float64) bool {
			return sample.Audio.Config.Generation != 0
		})
	return summary
}

func desktopAudioRequested(options protocol.RemoteDesktopConnectOptions) bool {
	return options.Audio == nil || *options.Audio
}

func summarizeAudioValidation(
	options protocol.RemoteDesktopConnectOptions,
	samples []DesktopDiagnosticSample,
	current DesktopAudioDiagnostics,
) *DesktopAudioValidationSummary {
	requested := desktopAudioRequested(options)
	if !requested && current.Config.Generation == 0 {
		return nil
	}
	summary := &DesktopAudioValidationSummary{
		Requested:      requested,
		RequestedCodec: strings.ToLower(strings.TrimSpace(options.AudioCodec)),
	}
	var (
		firstConfigured *DesktopDiagnosticSample
		lastConfigured  *DesktopDiagnosticSample
	)
	for i := range samples {
		sample := samples[i]
		if sample.Audio.Config.Generation == 0 {
			continue
		}
		summary.ConfiguredSamples++
		switch strings.ToLower(strings.TrimSpace(sample.Audio.Config.Codec)) {
		case protocol.DesktopAudioCodecOpus:
			summary.OpusSamples++
		case protocol.DesktopAudioCodecPCMS16LE:
			summary.PCMSamples++
			if summary.RequestedCodec == protocol.DesktopAudioCodecOpus {
				summary.PCMFallbackSamples++
			}
		}
		if sample.Audio.QueueFrames > summary.MaxQueueFrames {
			summary.MaxQueueFrames = sample.Audio.QueueFrames
		}
		if firstConfigured == nil {
			copy := sample
			firstConfigured = &copy
		}
		copy := sample
		lastConfigured = &copy
	}

	latest := current
	if latest.Config.Generation == 0 && lastConfigured != nil {
		latest = lastConfigured.Audio
	}
	if latest.Config.Generation == 0 {
		return summary
	}
	summary.Active = true
	summary.Codec = strings.ToLower(strings.TrimSpace(latest.Config.Codec))
	summary.SampleRate = latest.Config.SampleRate
	summary.Channels = latest.Config.Channels
	summary.BitsPerSample = latest.Config.BitsPerSample
	summary.FrameDurationMs = latest.Config.FrameDurationMs
	summary.TargetBitrate = latest.Config.TargetBitrate
	if latest.Config.SampleRate > 0 && latest.Config.Channels > 0 && latest.Config.BitsPerSample > 0 {
		summary.RawPCMBitrate = latest.Config.SampleRate * latest.Config.Channels * latest.Config.BitsPerSample
	}
	summary.ReceivedFrames = latest.ReceivedFrames
	summary.ConsumedFrames = latest.ConsumedFrames
	summary.ReceivedBytes = latest.ReceivedBytes
	summary.ConsumedBytes = latest.ConsumedBytes
	summary.QueueDroppedFrames = latest.QueueDroppedFrames
	summary.ConcealmentFrames = latest.ConcealmentFrames
	summary.GapSkippedFrames = latest.GapSkippedFrames
	summary.ReorderedFrames = latest.ReorderedFrames
	summary.DuplicateFrames = latest.DuplicateFrames
	summary.LateFrames = latest.LateFrames
	summary.PlayoutTimeoutFrames = latest.PlayoutTimeoutFrames
	if latest.QueueFrames > summary.MaxQueueFrames {
		summary.MaxQueueFrames = latest.QueueFrames
	}
	missing := latest.ConcealmentFrames + latest.GapSkippedFrames
	totalNetworkFrames := latest.ReceivedFrames + missing
	if totalNetworkFrames > 0 && missing > 0 {
		summary.EstimatedNetworkLossPercent = float64(missing) * 100 / float64(totalNetworkFrames)
	}
	if firstConfigured != nil && lastConfigured != nil &&
		lastConfigured.AtUnixMs > firstConfigured.AtUnixMs &&
		lastConfigured.Audio.ReceivedBytes >= firstConfigured.Audio.ReceivedBytes {
		summary.SampleSpanMs = lastConfigured.AtUnixMs - firstConfigured.AtUnixMs
		deltaBytes := lastConfigured.Audio.ReceivedBytes - firstConfigured.Audio.ReceivedBytes
		summary.ObservedPayloadBitrate = int64(deltaBytes) * 8 * 1000 / summary.SampleSpanMs
		if summary.ObservedPayloadBitrate > 0 && summary.RawPCMBitrate > 0 {
			summary.ObservedCompressionRatio = float64(summary.RawPCMBitrate) / float64(summary.ObservedPayloadBitrate)
		}
	}
	return summary
}

func summarizeHEVCValidation(
	options protocol.RemoteDesktopConnectOptions,
	samples []DesktopDiagnosticSample,
) *DesktopHEVCValidationSummary {
	if !strings.EqualFold(strings.TrimSpace(options.Codec), protocol.DesktopCodecH265Validation) {
		return nil
	}
	summary := &DesktopHEVCValidationSummary{
		Requested:       true,
		CaptureBackends: make(map[string]int),
		CaptureFormats:  make(map[string]int),
		EncoderBackends: make(map[string]int),
		DecoderBackends: make(map[string]int),
		RenderBackends:  make(map[string]int),
	}
	for _, sample := range samples {
		switch strings.ToLower(strings.TrimSpace(sample.Config.Codec)) {
		case "h265":
			summary.HEVCSamples++
			if sample.Stats.EncoderHardware {
				summary.HardwareEncodingSamples++
			}
			if sample.Stats.DecoderHardware {
				summary.HardwareDecodingSamples++
			}
			incrementDiagnosticCount(summary.CaptureBackends, sample.Stats.CaptureBackend)
			incrementDiagnosticCount(summary.CaptureFormats, sample.Stats.CaptureFormat)
			incrementDiagnosticCount(summary.EncoderBackends, sample.Stats.EncoderBackend)
			incrementDiagnosticCount(summary.DecoderBackends, sample.Stats.DecoderBackend)
			incrementDiagnosticCount(summary.RenderBackends, sample.Stats.RenderBackend)
		case "h264":
			summary.H264FallbackSamples++
		case "jpeg":
			summary.JPEGFallbackSamples++
		}
	}
	return summary
}

func desktopGPUFormatForConfig(config protocol.DesktopVideoConfig) string {
	switch strings.ToLower(strings.TrimSpace(config.Codec)) {
	case "h264", "h265":
	default:
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(config.Chroma), string(protocol.DesktopChroma444)) {
		return "ayuv"
	}
	return "nv12"
}

func desktopGPUFormatAdvertised(capability *protocol.DesktopGPUCapability, format string) bool {
	if capability == nil || format == "" ||
		!capability.EncodeZeroCopy || !capability.DecodeZeroCopy || !capability.DisplayZeroCopy {
		return false
	}
	for _, advertised := range capability.Formats {
		if strings.EqualFold(strings.TrimSpace(advertised), format) {
			return true
		}
	}
	return false
}

func desktopD3D11ZeroCopyBackend(backend string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(backend)), "-d3d11-zero-copy")
}

func desktopGPUHostEncodeZeroCopy(format string, stats protocol.DesktopSessionStats) bool {
	captureFormat := strings.ToLower(strings.TrimSpace(stats.CaptureFormat))
	backend := strings.ToLower(strings.TrimSpace(stats.EncoderBackend))
	switch format {
	case "ayuv":
		return captureFormat == "d3d11-ayuv" &&
			stats.EncoderHardware &&
			desktopD3D11ZeroCopyBackend(backend)
	case "nv12":
		return captureFormat == "d3d11-nv12" &&
			(backend == "media-foundation-d3d11" ||
				backend == "media-foundation-hevc-d3d11")
	default:
		return false
	}
}

func desktopGPUViewerDecodeZeroCopy(format string, stats protocol.DesktopSessionStats) bool {
	backend := strings.ToLower(strings.TrimSpace(stats.DecoderBackend))
	switch format {
	case "ayuv":
		return stats.DecoderHardware && desktopD3D11ZeroCopyBackend(backend)
	case "nv12":
		return backend == "media-foundation-d3d11-zero-copy"
	default:
		return false
	}
}

func desktopGPUViewerDisplayZeroCopy(stats protocol.DesktopSessionStats) bool {
	return strings.EqualFold(strings.TrimSpace(stats.RenderBackend), "d3d11-zero-copy")
}

func summarizeGPUValidation(
	targetGPU *protocol.DesktopGPUCapability,
	current protocol.DesktopVideoConfig,
	samples []DesktopDiagnosticSample,
) *DesktopGPUValidationSummary {
	expectedFormat := desktopGPUFormatForConfig(current)
	if expectedFormat == "" {
		for i := len(samples) - 1; i >= 0; i-- {
			expectedFormat = desktopGPUFormatForConfig(samples[i].Config)
			if expectedFormat != "" {
				break
			}
		}
	}
	if expectedFormat == "" && targetGPU == nil {
		return nil
	}
	summary := &DesktopGPUValidationSummary{
		ExpectedFormat:   expectedFormat,
		TargetAdvertised: desktopGPUFormatAdvertised(targetGPU, expectedFormat),
		CaptureFormats:   make(map[string]int),
		EncoderBackends:  make(map[string]int),
		DecoderBackends:  make(map[string]int),
		RenderBackends:   make(map[string]int),
	}
	for _, sample := range samples {
		if desktopGPUFormatForConfig(sample.Config) != expectedFormat {
			continue
		}
		summary.MatchingSamples++
		incrementDiagnosticCount(summary.CaptureFormats, sample.Stats.CaptureFormat)
		incrementDiagnosticCount(summary.EncoderBackends, sample.Stats.EncoderBackend)
		incrementDiagnosticCount(summary.DecoderBackends, sample.Stats.DecoderBackend)
		incrementDiagnosticCount(summary.RenderBackends, sample.Stats.RenderBackend)

		hostZeroCopy := desktopGPUHostEncodeZeroCopy(expectedFormat, sample.Stats)
		viewerDecodeZeroCopy := desktopGPUViewerDecodeZeroCopy(expectedFormat, sample.Stats)
		viewerDisplayZeroCopy := desktopGPUViewerDisplayZeroCopy(sample.Stats)
		if hostZeroCopy {
			summary.HostEncodeZeroCopySamples++
		}
		if viewerDecodeZeroCopy {
			summary.ViewerDecodeZeroCopySamples++
		}
		if viewerDisplayZeroCopy {
			summary.ViewerDisplayZeroCopySamples++
		}
		if hostZeroCopy && viewerDecodeZeroCopy && viewerDisplayZeroCopy {
			summary.EndToEndZeroCopySamples++
		}
	}
	summary.FallbackSamples = summary.MatchingSamples - summary.EndToEndZeroCopySamples
	return summary
}

func (r *sessionDiagnosticsRecorder) Report(
	now time.Time,
	config protocol.DesktopVideoConfig,
	stats protocol.DesktopSessionStats,
	audio DesktopAudioDiagnostics,
) DesktopDiagnosticsReport {
	if r == nil {
		return DesktopDiagnosticsReport{}
	}
	if now.IsZero() {
		now = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	samples := append([]DesktopDiagnosticSample(nil), r.samples...)
	return DesktopDiagnosticsReport{
		SchemaVersion:     desktopDiagnosticsSchemaVersion,
		TargetID:          r.targetID,
		Scene:             r.options.Scene,
		StartedAtUnixMs:   r.started.UnixMilli(),
		GeneratedAtUnixMs: now.UnixMilli(),
		SampleIntervalMs:  desktopDiagnosticsIntervalMs,
		Options:           r.options,
		CurrentConfig:     config,
		CurrentStats:      stats,
		CurrentAudio:      audio,
		Summary:           summarizeDesktopDiagnostics(r.started, now, samples),
		AudioValidation:   summarizeAudioValidation(r.options, samples, audio),
		HEVCValidation:    summarizeHEVCValidation(r.options, samples),
		TargetGPU:           protocol.CloneDesktopGPUCapability(r.targetGPU),
		TargetGPUCandidates: protocol.CloneDesktopGPUCandidateDiagnostics(r.targetGPUCandidates),
		GPUValidation:       summarizeGPUValidation(r.targetGPU, config, samples),
		Samples:           samples,
	}
}
