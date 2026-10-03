package app

import (
	"sort"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/traffic"
)

const maxDiagnosticConnections = 64

type DiagnosticsSnapshot struct {
	SampledAt   time.Time            `json:"sampledAt"`
	Status      AgentStatus          `json:"status"`
	Connections []traffic.Connection `json:"connections,omitempty"`
	Exit        *exit.Diagnostics    `json:"exit,omitempty"`
}

func (a *Agent) Diagnostics() DiagnosticsSnapshot {
	status := a.Status()
	connections := a.Connections().Connections
	active := make([]traffic.Connection, 0, len(connections))
	for _, connection := range connections {
		if connection.State == "active" {
			active = append(active, connection)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].DownloadRate != active[j].DownloadRate {
			return active[i].DownloadRate > active[j].DownloadRate
		}
		return active[i].Download > active[j].Download
	})
	if len(active) > maxDiagnosticConnections {
		active = active[:maxDiagnosticConnections]
	}
	a.mu.RLock()
	handler := a.exitHandler
	a.mu.RUnlock()
	snapshot := DiagnosticsSnapshot{
		SampledAt: time.Now(), Status: status, Connections: active,
	}
	if handler != nil {
		exitSnapshot := handler.Diagnostics()
		snapshot.Exit = &exitSnapshot
	}
	return snapshot
}
