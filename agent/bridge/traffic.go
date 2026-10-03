package bridge

import (
	"context"
	"strings"
	"time"

	"relayproxy/agent/app"
	"relayproxy/internal/traffic"
)

func (b *UIBridge) GetDiagnostics() app.DiagnosticsSnapshot { return b.agent.Diagnostics() }

func (b *UIBridge) GetConnections() traffic.Snapshot { return b.agent.Connections() }

func (b *UIBridge) ClearConnections() { b.agent.ClearConnections() }

func (b *UIBridge) RunSpeedTest(exitID string, durationSeconds int) (app.SpeedTestResult, error) {
	if durationSeconds <= 0 {
		durationSeconds = 3
	}
	exitID = strings.TrimSpace(exitID)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSeconds*2+30)*time.Second)
	defer cancel()
	return b.agent.RunSpeedTest(ctx, exitID, time.Duration(durationSeconds)*time.Second)
}
