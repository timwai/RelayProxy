package tunnel

import (
	"sync/atomic"
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

const (
	pipePhaseIdle uint32 = iota
	pipePhaseRead
	pipePhaseWrite
)

type pipePumpMetrics struct {
	phase        atomic.Uint32
	startedNanos atomic.Int64
	readNanos    atomic.Uint64
	writeNanos   atomic.Uint64
	readCalls    atomic.Uint64
	writeCalls   atomic.Uint64
	readBytes    atomic.Uint64
	writeBytes   atomic.Uint64
}

// PipeMetrics records approximate elapsed time and exact byte/call counters for
// both copy pumps. The hot path is lock-free and uses the process-wide coarse
// clock so high-throughput proxy streams don't pay a mutex plus time.Now on
// every copied chunk. Snapshot uses a precise clock for the currently blocked
// operation, while completed operation timing has coarse-clock granularity.
type PipeMetrics struct {
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
	p := m.pump(up)
	p.startedNanos.Store(coarseTimeNanos())
	switch phase {
	case "read":
		p.readCalls.Add(1)
		p.phase.Store(pipePhaseRead)
	case "write":
		p.writeCalls.Add(1)
		p.phase.Store(pipePhaseWrite)
	default:
		p.phase.Store(pipePhaseIdle)
	}
}

func (m *PipeMetrics) end(up bool, n int) {
	p := m.pump(up)
	now := coarseTimeNanos()
	phase := p.phase.Swap(pipePhaseIdle)
	started := p.startedNanos.Swap(0)
	var elapsed uint64
	if started > 0 && now > started {
		elapsed = uint64(now - started)
	}
	switch phase {
	case pipePhaseRead:
		p.readNanos.Add(elapsed)
		if n > 0 {
			p.readBytes.Add(uint64(n))
		}
	case pipePhaseWrite:
		p.writeNanos.Add(elapsed)
		if n > 0 {
			p.writeBytes.Add(uint64(n))
		}
	}
}

func (m *PipeMetrics) Snapshot() PipeMetricsSnapshot {
	now := time.Now().UnixNano()
	snapshot := func(p *pipePumpMetrics) PumpMetrics {
		readNanos := p.readNanos.Load()
		writeNanos := p.writeNanos.Load()
		out := PumpMetrics{
			ReadCalls:  p.readCalls.Load(),
			WriteCalls: p.writeCalls.Load(),
			ReadBytes:  p.readBytes.Load(),
			WriteBytes: p.writeBytes.Load(),
		}

		phase := p.phase.Load()
		started := p.startedNanos.Load()
		if started > 0 && now > started {
			elapsed := uint64(now - started)
			switch phase {
			case pipePhaseRead:
				readNanos += elapsed
			case pipePhaseWrite:
				writeNanos += elapsed
			}
		}
		switch phase {
		case pipePhaseRead:
			out.Phase = "read"
		case pipePhaseWrite:
			out.Phase = "write"
		default:
			out.Phase = "idle"
		}
		out.ReadMS = float64(readNanos) / float64(time.Millisecond)
		out.WriteMS = float64(writeNanos) / float64(time.Millisecond)
		return out
	}
	return PipeMetricsSnapshot{Up: snapshot(&m.up), Down: snapshot(&m.down)}
}
