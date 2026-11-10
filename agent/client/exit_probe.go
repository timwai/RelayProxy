package client

import (
	"context"
	"errors"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/speedtest"
	"relayproxy/internal/tunnel"
)

type ExitProbeResult struct {
	ExitID    string                  `json:"exitId"`
	Path      string                  `json:"path"`
	LatencyMS int64                   `json:"latencyMs"`
	PathInfo  protocol.ConnectionPath `json:"pathInfo"`
}

// ProbeExit measures an authenticated application response from one exit.
// It uses the existing upload handshake with zero payload, never an Internet
// target or a bandwidth flood. Elapsed time includes stream establishment.
func (d *TunnelDialer) ProbeExit(ctx context.Context, exitID string) (ExitProbeResult, error) {
	if exitID == "" {
		return ExitProbeResult{}, errors.New("未指定检测出口")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	selected, stream, err := d.openProxyStream(ctx, exitID)
	if err != nil {
		return ExitProbeResult{}, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = stream.SetDeadline(deadline)
	id := d.nextRequestIDWithPrefix("probe_")
	clientID := ""
	if d.getClientID != nil {
		clientID = d.getClientID()
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeSpeedTest, RequestID: id, ExitDeviceID: exitID, ClientDeviceID: clientID,
	}); err != nil {
		return ExitProbeResult{}, err
	}
	if err := protocol.WriteJSON(stream, protocol.SpeedTestRequest{
		RequestID: id, Direction: protocol.SpeedTestUpload, DurationMS: int(speedtest.MinDuration.Milliseconds()),
	}); err != nil {
		return ExitProbeResult{}, err
	}
	var ack protocol.SpeedTestResponse
	if err := protocol.ReadJSON(stream, &ack); err != nil {
		return ExitProbeResult{}, err
	}
	if !ack.Success {
		if ack.ErrorMessage == "" {
			ack.ErrorMessage = "出口拒绝延迟检测"
		}
		return ExitProbeResult{}, protocol.NewRelayError(ack.ErrorCode, ack.ErrorMessage)
	}
	if ack.RequestID != id {
		return ExitProbeResult{}, errors.New("出口检测响应不匹配")
	}
	latency := max(1, time.Since(started).Milliseconds())
	if err := stream.CloseWrite(); err != nil {
		return ExitProbeResult{}, err
	}
	var complete protocol.SpeedTestComplete
	if err := protocol.ReadJSON(stream, &complete); err != nil {
		return ExitProbeResult{}, err
	}
	if complete.Bytes != 0 {
		return ExitProbeResult{}, errors.New("出口检测响应无效")
	}
	if err := ctx.Err(); err != nil {
		return ExitProbeResult{}, err
	}
	return ExitProbeResult{ExitID: exitID, Path: selected.Path.String(), LatencyMS: latency, PathInfo: sessionPathInfo(selected.Session, selected.Path)}, nil
}
