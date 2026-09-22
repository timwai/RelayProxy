package desktop

import (
	"sync"
	"time"

	desktopadapt "relayproxy/agent/desktop/adapt"
	"relayproxy/internal/protocol"
)

const (
	desktopDiagnosticsSchemaVersion = 1
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
		Samples:           samples,
	}
}
