package exit

import (
	"sort"
	"time"

	"relayproxy/internal/tunnel"
)

const maxDiagnosticTCP = 64

type TCPDiagnostic struct {
	ID             uint64             `json:"id"`
	Host           string             `json:"host"`
	Port           uint16             `json:"port"`
	Remote         string             `json:"remote"`
	StartedAt      time.Time          `json:"started_at"`
	TunnelToTarget tunnel.PumpMetrics `json:"tunnel_to_target"`
	TargetToTunnel tunnel.PumpMetrics `json:"target_to_tunnel"`
	pipeSource     *tunnel.PipeMetrics
}

type Diagnostics struct {
	SampledAt time.Time       `json:"sampled_at"`
	ActiveTCP []TCPDiagnostic `json:"active_tcp"`
}

func (h *Handler) beginTCPDiagnostic(host string, port uint16, remote string, metrics *tunnel.PipeMetrics) uint64 {
	h.diagnosticsMu.Lock()
	h.diagnosticsNext++
	id := h.diagnosticsNext
	h.diagnosticsTCP[id] = &TCPDiagnostic{
		ID: id, Host: host, Port: port, Remote: remote, StartedAt: time.Now(), pipeSource: metrics,
	}
	h.diagnosticsMu.Unlock()
	return id
}

func (h *Handler) endTCPDiagnostic(id uint64) {
	h.diagnosticsMu.Lock()
	delete(h.diagnosticsTCP, id)
	h.diagnosticsMu.Unlock()
}

func (h *Handler) Diagnostics() Diagnostics {
	h.diagnosticsMu.Lock()
	active := make([]TCPDiagnostic, 0, len(h.diagnosticsTCP))
	for _, item := range h.diagnosticsTCP {
		copy := *item
		if item.pipeSource != nil {
			pipe := item.pipeSource.Snapshot()
			copy.TunnelToTarget = pipe.Up
			copy.TargetToTunnel = pipe.Down
		}
		copy.pipeSource = nil
		active = append(active, copy)
	}
	h.diagnosticsMu.Unlock()
	sort.Slice(active, func(i, j int) bool {
		if active[i].TargetToTunnel.ReadBytes != active[j].TargetToTunnel.ReadBytes {
			return active[i].TargetToTunnel.ReadBytes > active[j].TargetToTunnel.ReadBytes
		}
		return active[i].ID < active[j].ID
	})
	if len(active) > maxDiagnosticTCP {
		active = active[:maxDiagnosticTCP]
	}
	return Diagnostics{SampledAt: time.Now(), ActiveTCP: active}
}
