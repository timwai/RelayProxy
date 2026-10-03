package tunnel

import (
	"sync"
	"time"
)

type PumpMetrics struct {
	Phase      string  `json:"phase"`
	ReadMS     float64 `json:"read_ms"`
	WriteMS    float64 `json:"write_ms"`
	ReadCalls  uint64  `json:"read_calls"`
	WriteCalls uint64  `json:"write_calls"`
	ReadBytes  uint64  `json:"read_bytes"`
	WriteBytes uint64  `json:"write_bytes"`
}

type PipeMetricsSnapshot struct {
	Up   PumpMetrics `json:"up"`
	Down PumpMetrics `json:"down"`
}

type pipePumpMetrics struct {
	PumpMetrics
	started time.Time
}

// PipeMetrics records elapsed time inside the active Read or Write call. It is
// safe to snapshot while both Pipe pumps are running.
type PipeMetrics struct {
	mu   sync.Mutex
	up   pipePumpMetrics
	down pipePumpMetrics
}

func (m *PipeMetrics) pump(up bool) *pipePumpMetrics {
	if up {
		return &m.up
	}
	return &m.down
}

func (m *PipeMetrics) begin(up bool, phase string) {
	m.mu.Lock()
	p := m.pump(up)
	p.Phase, p.started = phase, time.Now()
	if phase == "read" {
		p.ReadCalls++
	} else {
		p.WriteCalls++
	}
	m.mu.Unlock()
}

func (m *PipeMetrics) end(up bool, n int) {
	now := time.Now()
	m.mu.Lock()
	p := m.pump(up)
	elapsed := now.Sub(p.started).Seconds() * 1000
	if p.Phase == "read" {
		p.ReadMS += elapsed
		if n > 0 {
			p.ReadBytes += uint64(n)
		}
	} else if p.Phase == "write" {
		p.WriteMS += elapsed
		if n > 0 {
			p.WriteBytes += uint64(n)
		}
	}
	p.Phase, p.started = "idle", time.Time{}
	m.mu.Unlock()
}

func (m *PipeMetrics) Snapshot() PipeMetricsSnapshot {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := func(p pipePumpMetrics) PumpMetrics {
		out := p.PumpMetrics
		if !p.started.IsZero() {
			elapsed := now.Sub(p.started).Seconds() * 1000
			if p.Phase == "read" {
				out.ReadMS += elapsed
			} else if p.Phase == "write" {
				out.WriteMS += elapsed
			}
		}
		return out
	}
	return PipeMetricsSnapshot{Up: snapshot(m.up), Down: snapshot(m.down)}
}
