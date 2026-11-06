package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"relayproxy/internal/exitgroup"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const exitSystemDNSRequestTimeout = 8 * time.Second

// ExchangeExitSystemDNS requests a single A/AAAA lookup from the selected
// device's OS resolver. It never opens a local DNS socket or falls back to
// local resolution when the remote path fails.
func (d *TunnelDialer) ExchangeExitSystemDNS(ctx context.Context, id string, query []byte) ([]byte, error) {
	if _, _, err := protocol.ValidateExitDNSQuestion(query); err != nil {
		return nil, err
	}
	if id == "" {
		id = d.GetDefaultExitID()
	}
	if strings.HasPrefix(id, "local:") {
		return nil, fmt.Errorf("exit_system requires a RelayProxy device exit (not a custom proxy)")
	}
	if !exitgroup.IsID(id) {
		return d.exchangeExitSystemDNS(ctx, id, query)
	}
	var failures []error
	for _, member := range d.groupCandidates(id) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempt, cancel := context.WithTimeout(ctx, exitSystemDNSRequestTimeout)
		answer, err := d.exchangeExitSystemDNS(attempt, member, query)
		cancel()
		if err == nil {
			return answer, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", member, err))
		if terminalGroupError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("exit group %s has no system DNS resolver: %w", id, errors.Join(failures...))
}

func (d *TunnelDialer) exchangeExitSystemDNS(parent context.Context, exitID string, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, exitSystemDNSRequestTimeout)
	defer cancel()
	_, stream, err := d.openProxyStream(ctx, exitID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	stop := tunnel.InterruptOnCancel(ctx, stream)
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	requestID := d.nextRequestIDWithPrefix("dns_")
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeExitSystemDNS, ExitDeviceID: exitID, RequestID: requestID,
	}); err != nil {
		return nil, fmt.Errorf("unable to send exit DNS header: %w", err)
	}
	if err := protocol.WriteJSON(stream, protocol.ExitDNSRequest{
		RequestID: requestID, Question: query,
	}); err != nil {
		return nil, fmt.Errorf("unable to send exit DNS question: %w", err)
	}
	var response protocol.ExitDNSResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return nil, fmt.Errorf("unable to read exit DNS result: %w", err)
	}
	if response.RequestID != requestID {
		return nil, fmt.Errorf("exit DNS response request identifier mismatch")
	}
	if !response.Success {
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeDNSFailed
		}
		return nil, protocol.NewRelayError(code, response.ErrorMessage)
	}
	if _, err := protocol.ValidateExitDNSAnswer(query, response.Answer); err != nil {
		return nil, fmt.Errorf("untrusted exit DNS response: %w", err)
	}
	return response.Answer, nil
}
