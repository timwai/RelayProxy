package client

import (
	"context"
	"fmt"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// DialDesktopMedia opens the Relay-only native datagram path used by Relay
// Desktop video/audio/cursor media. Reliable stream fallback is intentionally
// forbidden to avoid head-of-line blocking for interactive video.
func (d *TunnelDialer) DialDesktopMedia(ctx context.Context, targetDeviceID string) (*desktopmedia.MediaConn, error) {
	return d.DialDesktopMediaWithOptions(ctx, targetDeviceID, protocol.RemoteDesktopConnectOptions{})
}

// DialDesktopMediaWithOptions carries controller media preferences to the
// authorized Host as part of the existing Relay Desktop media handshake.
func (d *TunnelDialer) DialDesktopMediaWithOptions(ctx context.Context, targetDeviceID string, options protocol.RemoteDesktopConnectOptions) (*desktopmedia.MediaConn, error) {
	return d.DialDesktopMediaForSessionWithOptions(ctx, targetDeviceID, "", options)
}

// DialDesktopMediaForSessionWithOptions is the session-scoped Relay Desktop
// handshake used by independent multi-window sessions. The logical desktop
// session ID is forwarded unchanged to the target so its Relay and P2P media
// paths can be associated without relying on ControllerID alone.
func (d *TunnelDialer) DialDesktopMediaForSessionWithOptions(ctx context.Context, targetDeviceID, desktopSessionID string, options protocol.RemoteDesktopConnectOptions) (*desktopmedia.MediaConn, error) {
	sess := d.getTunnel()
	if sess == nil {
		return nil, fmt.Errorf("tunnel is not connected")
	}
	if targetDeviceID == "" {
		return nil, fmt.Errorf("Relay Desktop target device id is required")
	}
	if !tunnel.PeerSupportsDatagrams(sess) {
		return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "Relay Desktop media requires QUIC Datagram")
	}
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Relay Desktop media stream: %w", err)
	}
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	datagrams, err := tunnel.OpenDesktopDatagramChannel(sess, 0)
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = datagrams.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	}
	reqID := d.nextRequestIDWithPrefix("desktop_")
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenDesktopMedia,
		RequestID: reqID, ExitDeviceID: targetDeviceID,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	if err := protocol.WriteJSON(stream, protocol.OpenDesktopMediaRequest{
		RequestID: reqID, TimeoutMs: 10000, Mode: protocol.DesktopMediaModeDatagram, AssociationID: datagrams.ID,
		DesktopSessionID: desktopSessionID, Options: &options,
	}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	var response protocol.OpenDesktopMediaResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("read Relay Desktop media response: %w", err)
	}
	if !response.Success || response.Mode != protocol.DesktopMediaModeDatagram || response.AssociationID != datagrams.ID {
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
	keep = true
	return desktopmedia.NewMediaConn(datagrams, stream), nil
}
