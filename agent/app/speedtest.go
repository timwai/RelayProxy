package app

import (
	"context"
	"time"

	"relayproxy/agent/client"
)

type SpeedTestResult = client.SpeedTestResult

func (a *Agent) RunSpeedTest(ctx context.Context, exitID string, duration time.Duration) (SpeedTestResult, error) {
	return a.rawDialer.RunSpeedTest(ctx, exitID, duration)
}
