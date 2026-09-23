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
	desktopDiagnosticsSchemaVersion = 3
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
	Summary           DesktopDiagnosticsSummary            `json:"summary"`
	HEVCValidation    *DesktopHEVCValidationSummary         `json:"hevcValidation,omitempty"`
	Samples           []DesktopDiagnosticSample            `json:"samples"`
}

type sessionDiagnosticsRecorder struct {
	mu       sync.Mutex
	targetID string
	options  protocol.RemoteDesktopConnectOptions
	started  time.Time
	samples  []DesktopDiagnosticSample
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
		CaptureBackends:   make(map[string]int),
		CaptureFormats:    make(map[string]int),
		EncoderBackends:   make(map[string]int),
		DecoderBackends:   make(map[string]int),
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
		if sample.Config.Width > 0 && sample.Config.Height > 0 {
			incrementDiagnosticCount(summary.Resolutions,
				fmt.Sprintf("%dx%d", sample.Config.Width, sample.Config.Height))
		}
		incrementDiagnosticCount(summary.CaptureBackends, sample.Stats.CaptureBackend)
		incrementDiagnosticCount(summary.CaptureFormats, sample.Stats.CaptureFormat)
		incrementDiagnosticCount(summary.EncoderBackends, sample.Stats.EncoderBackend)
		incrementDiagnosticCount(summary.DecoderBackends, sample.Stats.DecoderBackend)
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
		case "h264":
			summary.H264FallbackSamples++
		case "jpeg":
			summary.JPEGFallbackSamples++
		}
	}
	return summary
}

func (r *sessionDiagnosticsRecorder) Report(
	now time.Time,
	config protocol.DesktopVideoConfig,
	stats protocol.DesktopSessionStats,
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
		Summary:           summarizeDesktopDiagnostics(r.started, now, samples),
		HEVCValidation:    summarizeHEVCValidation(r.options, samples),
		Samples:           samples,
	}
}
