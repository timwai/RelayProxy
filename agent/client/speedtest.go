package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/speedtest"
)

type SpeedTestDirectionResult struct {
	Path string `json:"path"`
	speedtest.Measurement
}

type SpeedTestResult struct {
	ExitID     string                   `json:"exitId"`
	StartedAt  time.Time                `json:"startedAt"`
	FinishedAt time.Time                `json:"finishedAt"`
	Upload     SpeedTestDirectionResult `json:"upload"`
	Download   SpeedTestDirectionResult `json:"download"`
}

func (d *TunnelDialer) runSpeedTestDirection(ctx context.Context, exitID, direction string, duration time.Duration) (SpeedTestDirectionResult, error) {
	_, stream, path, err := d.openProxyStream(ctx, exitID)
	if err != nil {
		return SpeedTestDirectionResult{}, err
	}
	defer stream.Close()
	requestID := d.nextRequestIDWithPrefix("speed_")
	clientID := ""
	if d.getClientID != nil {
		clientID = d.getClientID()
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeSpeedTest, RequestID: requestID,
		ClientDeviceID: clientID, ExitDeviceID: exitID,
	}); err != nil {
		return SpeedTestDirectionResult{}, err
	}
	measurement, err := speedtest.Run(ctx, stream, protocol.SpeedTestRequest{
		RequestID: requestID, Direction: direction, DurationMS: int(duration / time.Millisecond),
	})
	return SpeedTestDirectionResult{Path: path.String(), Measurement: measurement}, err
}

// RunSpeedTest measures both directions through one authorized exit. Each
// direction uses its own stream so half-closes and elapsed time are unambiguous.
func (d *TunnelDialer) RunSpeedTest(ctx context.Context, exitID string, duration time.Duration) (SpeedTestResult, error) {
	exitID = strings.TrimSpace(exitID)
	if exitID == "" {
		exitID = d.GetDefaultExitID()
	}
	if exitID == "" {
		return SpeedTestResult{}, errors.New("未指定测速出口")
	}
	result := SpeedTestResult{ExitID: exitID, StartedAt: time.Now()}
	upload, err := d.runSpeedTestDirection(ctx, exitID, protocol.SpeedTestUpload, duration)
	if err != nil {
		return result, fmt.Errorf("上行测速失败: %w", err)
	}
	result.Upload = upload
	download, err := d.runSpeedTestDirection(ctx, exitID, protocol.SpeedTestDownload, duration)
	if err != nil {
		return result, fmt.Errorf("下行测速失败: %w", err)
	}
	result.Download = download
	result.FinishedAt = time.Now()
	return result, nil
}
