package rdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const defaultTargetAddress = "127.0.0.1:3389"

func targetAddress(address string) string {
	if address == "" {
		return defaultTargetAddress
	}
	return address
}

// HandleTargetStream accepts only gateway-created RDP frames. It never reads a
// controller-provided host or port; the address comes from the target Agent's
// own configuration.
func HandleTargetStream(ctx context.Context, stream tunnel.TunnelStream, sess tunnel.TunnelSession, address string) error {
	if err := stream.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		_ = stream.Close()
		return err
	}
	header, err := protocol.ReadStreamHeader(stream)
	if err != nil {
		_ = stream.Close()
		return err
	}
	return HandleTargetStreamWithHeader(ctx, stream, sess, address, header)
}

// HandleTargetStreamWithHeader handles a stream after the caller consumed its
// protocol header. This lets the agent share one AcceptStream dispatcher with
// the regular exit proxy without racing two AcceptStream waiters.
func HandleTargetStreamWithHeader(ctx context.Context, stream tunnel.TunnelStream, sess tunnel.TunnelSession, address string, header *protocol.StreamHeader) error {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if header.Type != protocol.FrameTypeOpenRDP && header.Type != protocol.FrameTypeOpenRDPUDP {
		return errors.New("unsupported RDP target stream type")
	}
	var req protocol.OpenRDPRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		return err
	}
	if header.Type == protocol.FrameTypeOpenRDP {
		return handleTargetTCP(ctx, stream, req, address)
	}
	return handleTargetUDP(ctx, stream, sess, req, address)
}

func handleTargetTCP(ctx context.Context, stream tunnel.TunnelStream, req protocol.OpenRDPRequest, address string) error {
	local, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", targetAddress(address))
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeConnectionRefused, ErrorMessage: "本机 RDP 服务不可用"})
		return fmt.Errorf("dial local RDP TCP: %w", err)
	}
	tunnel.TuneTCPConn(local)
	defer local.Close()
	if err := protocol.WriteJSON(stream, protocol.OpenTCPResponse{RequestID: req.RequestID, Success: true, RemoteIP: targetAddress(address)}); err != nil {
		return err
	}
	_ = stream.SetDeadline(time.Time{})
	_, _ = tunnel.Pipe(ctx, stream, local, 10*time.Minute, nil)
	return nil
}

func handleTargetUDP(ctx context.Context, stream tunnel.TunnelStream, sess tunnel.TunnelSession, req protocol.OpenRDPRequest, address string) error {
	if req.Mode != protocol.UDPModeDatagram || req.AssociationID == 0 || sess == nil || !tunnel.PeerSupportsDatagrams(sess) {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: "本机 RDP UDP 需要 QUIC Datagram"})
		return errors.New("RDP UDP datagrams are unavailable")
	}
	channel, err := tunnel.OpenDatagramChannel(sess, req.AssociationID)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: err.Error()})
		return err
	}
	localAddr, err := net.ResolveUDPAddr("udp", targetAddress(address))
	if err != nil {
		_ = channel.Close()
		return err
	}
	local, err := net.DialUDP("udp", nil, localAddr)
	if err != nil {
		_ = channel.Close()
		_ = protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeConnectionRefused, ErrorMessage: "本机 RDP UDP 服务不可用"})
		return err
	}
	tunnel.TuneUDPConn(local)
	defer local.Close()
	remote := tunnel.NewUDPDatagramConnWithIdleTimeout(channel, stream, localAddr, 0)
	if err := protocol.WriteJSON(stream, protocol.OpenUDPResponse{RequestID: req.RequestID, Success: true, Mode: protocol.UDPModeDatagram, AssociationID: channel.ID, RemoteIP: localAddr.String()}); err != nil {
		_ = remote.Close()
		return err
	}
	_ = stream.SetDeadline(time.Time{})
	return bridgePacket(ctx, local, remote)
}

func bridgePacket(ctx context.Context, local net.PacketConn, remote net.PacketConn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer local.Close()
	defer remote.Close()
	result := make(chan error, 2)
	forward := func(dst net.PacketConn, src net.PacketConn, connectedDestination bool) {
		var write func([]byte, net.Addr) (int, error)
		if connectedDestination {
			writer, ok := dst.(interface{ Write([]byte) (int, error) })
			if !ok {
				result <- errors.New("connected UDP destination does not support Write")
				return
			}
			write = func(payload []byte, _ net.Addr) (int, error) {
				return writer.Write(payload)
			}
		} else {
			write = dst.WriteTo
		}
		buffer := make([]byte, 64*1024)
		for {
			n, addr, err := src.ReadFrom(buffer)
			if err != nil {
				result <- err
				return
			}
			// The target-side RDP socket is created with net.DialUDP, so it is
			// connected. Calling WriteTo on a connected UDP socket is rejected by
			// the Go runtime (ErrWriteToConnected), which prevented every packet
			// from reaching the target and made mstsc fall back to TCP. Use the
			// connected Write method for that direction; the relay PacketConn still
			// needs WriteTo because it carries no fixed peer address.
			if _, err := write(buffer[:n], addr); err != nil {
				result <- err
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
	go forward(remote, local, false)
	go forward(local, remote, true)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		return err
	}
}
