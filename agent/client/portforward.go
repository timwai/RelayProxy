package client

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"relayproxy/agent/portforward"
	"relayproxy/internal/protocol"
)

// PortForwardTransport adapts the existing authenticated proxy data path to
// the generic local mapping manager. The remote device must currently be an
// approved Proxy Exit; dedicated port-forward grants and target-side mapping
// authorization are added in the next control-plane integration step.
type PortForwardTransport struct {
	dialer *TunnelDialer
}

func NewPortForwardTransport(dialer *TunnelDialer) *PortForwardTransport {
	return &PortForwardTransport{dialer: dialer}
}

func (t *PortForwardTransport) DialTCP(ctx context.Context, mapping portforward.Mapping) (net.Conn, portforward.Path, error) {
	if t == nil || t.dialer == nil {
		return nil, portforward.PathUnknown, errors.New("port forward tunnel dialer is unavailable")
	}
	conn, err := t.dialer.dialTCP(ctx, mapping.RemoteDeviceID, mapping.TargetAddress, mapping.TargetPort)
	if err != nil {
		return nil, portforward.PathUnknown, err
	}
	return conn, portForwardPathFromConn(conn), nil
}

func (t *PortForwardTransport) OpenUDP(ctx context.Context, mapping portforward.Mapping) (portforward.DatagramSession, portforward.Path, error) {
	if t == nil || t.dialer == nil {
		return nil, portforward.PathUnknown, errors.New("port forward tunnel dialer is unavailable")
	}
	conn, err := t.dialer.DialUDPWithOptions(ctx, mapping.RemoteDeviceID, mapping.TargetAddress, mapping.TargetPort, UDPDialOptions{
		DatagramRequired: true,
	})
	if err != nil {
		return nil, portforward.PathUnknown, err
	}
	mode, ok := conn.(interface{ UDPMode() string })
	if !ok || mode.UDPMode() != protocol.UDPModeDatagram {
		_ = conn.Close()
		return nil, portforward.PathUnknown, protocol.NewRelayError(
			protocol.ErrCodeDatagramRequired,
			"port forwarding requires native UDP datagrams; reliable stream fallback is disabled",
		)
	}
	return &packetDatagramSession{conn: conn}, portForwardPathFromConn(conn), nil
}

type proxyPathCarrier interface {
	ProxyPath() string
}

func portForwardPathFromConn(conn any) portforward.Path {
	carrier, ok := conn.(proxyPathCarrier)
	if !ok {
		return portforward.PathUnknown
	}
	switch protocol.ProxyPath(carrier.ProxyPath()) {
	case protocol.ProxyPathPublicDirectQUIC:
		return portforward.PathPublicDirectQUIC
	case protocol.ProxyPathP2PQUIC:
		return portforward.PathP2PQUIC
	case protocol.ProxyPathRelayQUIC:
		return portforward.PathRelayQUIC
	case protocol.ProxyPathRelayTLS:
		return portforward.PathRelayTLS
	default:
		return portforward.PathUnknown
	}
}

type packetDatagramSession struct {
	conn net.PacketConn
	once sync.Once
}

func (s *packetDatagramSession) Send(ctx context.Context, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := s.conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() {
		_ = s.conn.SetWriteDeadline(time.Now())
	})
	n, err := s.conn.WriteTo(payload, nil)
	stop()
	_ = s.conn.SetWriteDeadline(time.Time{})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if n != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *packetDatagramSession) Receive(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := s.conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stop := context.AfterFunc(ctx, func() {
		_ = s.conn.SetReadDeadline(time.Now())
	})
	buffer := make([]byte, 65535)
	n, _, err := s.conn.ReadFrom(buffer)
	stop()
	_ = s.conn.SetReadDeadline(time.Time{})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), buffer[:n]...), nil
}

func (s *packetDatagramSession) Close() error {
	var closeErr error
	s.once.Do(func() { closeErr = s.conn.Close() })
	return closeErr
}
