package desktop

import (
	"context"
	"errors"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// HostHandler consumes one accepted Relay Desktop media association. A handler
// normally starts or attaches the capture/encoder pipeline and blocks until the
// session ends.
type HostHandler interface {
	HandleDesktopMedia(context.Context, *desktopmedia.MediaConn, protocol.RemoteDesktopConnectOptions) error
}

type HostHandlerFunc func(context.Context, *desktopmedia.MediaConn, protocol.RemoteDesktopConnectOptions) error

func (f HostHandlerFunc) HandleDesktopMedia(ctx context.Context, conn *desktopmedia.MediaConn, options protocol.RemoteDesktopConnectOptions) error {
	return f(ctx, conn, options)
}

// HandleTargetMediaStreamWithHeader negotiates the target half of a Relay
// Desktop native-datagram path. It never reports success without a registered
// Host backend.
func HandleTargetMediaStreamWithHeader(ctx context.Context, stream tunnel.TunnelStream, sess tunnel.TunnelSession, header *protocol.StreamHeader, handler HostHandler) error {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if header == nil || header.Type != protocol.FrameTypeOpenDesktopMedia {
		return errors.New("unsupported Relay Desktop target stream type")
	}
	var req protocol.OpenDesktopMediaRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		return err
	}
	deny := func(code, message string) error {
		_ = protocol.WriteJSON(stream, protocol.OpenDesktopMediaResponse{
			RequestID: req.RequestID, ErrorCode: code, ErrorMessage: message,
		})
		return errors.New(message)
	}
	if handler == nil {
		return deny(protocol.ErrCodeConnectionRefused, "Relay Desktop host backend is unavailable")
	}
	if req.Mode != protocol.DesktopMediaModeDatagram || req.AssociationID == 0 || sess == nil || !tunnel.PeerSupportsDatagrams(sess) {
		return deny(protocol.ErrCodeDatagramRequired, "Relay Desktop media requires QUIC Datagram")
	}
	channel, err := tunnel.OpenDesktopDatagramChannel(sess, req.AssociationID)
	if err != nil {
		return deny(protocol.ErrCodeDatagramRequired, err.Error())
	}
	conn := desktopmedia.NewMediaConn(channel, stream)
	defer conn.Close()
	if err := protocol.WriteJSON(stream, protocol.OpenDesktopMediaResponse{
		RequestID: req.RequestID, Success: true, Mode: protocol.DesktopMediaModeDatagram, AssociationID: channel.ID,
	}); err != nil {
		return err
	}
	_ = stream.SetDeadline(time.Time{})
	var options protocol.RemoteDesktopConnectOptions
	if req.Options != nil {
		options = *req.Options
	}
	return handler.HandleDesktopMedia(ctx, conn, options)
}
