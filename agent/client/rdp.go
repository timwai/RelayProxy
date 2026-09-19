package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// DialRDPTCP opens one fixed-target RDP TCP logical stream. The target device
// ID is the only destination supplied by the controller; host and port never
// cross this API boundary.
func (d *TunnelDialer) DialRDPTCP(ctx context.Context, targetDeviceID string) (net.Conn, error) {
	sess := d.getTunnel()
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	if targetDeviceID == "" {
		return nil, fmt.Errorf("RDP target device id is required")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open RDP TCP stream: %w", err)
	}
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	}
	reqID := d.nextRequestIDWithPrefix("rdp_")
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenRDP,
		RequestID: reqID, ExitDeviceID: targetDeviceID,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := protocol.WriteJSON(stream, protocol.OpenRDPRequest{RequestID: reqID, TimeoutMs: 10000}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("read RDP TCP response: %w", err)
	}
	if !response.Success {
		_ = stream.Close()
		return nil, protocol.NewRelayError(response.ErrorCode, response.ErrorMessage)
	}
	stopCancel()
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})
	return tunnel.NewNetConnAdapter(stream, sess.LocalAddr(), proxyAddr{net: "tcp", addr: targetDeviceID}), nil
}

// DialRDPUDP opens the unreliable RDP UDP path. It intentionally has no
// reliable-stream fallback: TLS/TCP tunnel sessions must make the controller
// use RDP TCP only to avoid head-of-line blocking.
func (d *TunnelDialer) DialRDPUDP(ctx context.Context, targetDeviceID string) (net.PacketConn, error) {
	sess := d.getTunnel()
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	if targetDeviceID == "" {
		return nil, fmt.Errorf("RDP target device id is required")
	}
	if !tunnel.PeerSupportsDatagrams(sess) {
		return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "RDP UDP is disabled on the current transport")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open RDP UDP stream: %w", err)
	}
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	datagrams, err := tunnel.OpenDatagramChannel(sess, 0)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	keepDatagrams := false
	defer func() {
		if !keepDatagrams {
			_ = datagrams.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	}
	reqID := d.nextRequestIDWithPrefix("rdp_")
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenRDPUDP,
		RequestID: reqID, ExitDeviceID: targetDeviceID,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := protocol.WriteJSON(stream, protocol.OpenRDPRequest{RequestID: reqID, TimeoutMs: 10000, Mode: protocol.UDPModeDatagram, AssociationID: datagrams.ID, DatagramRequired: true}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	var response protocol.OpenUDPResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("read RDP UDP response: %w", err)
	}
	if !response.Success || response.Mode != protocol.UDPModeDatagram || response.AssociationID != datagrams.ID {
		_ = stream.Close()
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeDatagramRequired
		}
		return nil, protocol.NewRelayError(code, response.ErrorMessage)
	}
	stopCancel()
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.SetDeadline(time.Time{})
	keepDatagrams = true
	return tunnel.NewUDPDatagramConnWithIdleTimeout(datagrams, stream, proxyAddr{net: "udp", addr: targetDeviceID}, 0), nil
}
