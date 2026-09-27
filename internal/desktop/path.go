package desktop

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"relayproxy/internal/protocol"
)

type DatagramPath interface {
	Name() string
	Send(context.Context, []byte) error
	Receive(context.Context) ([]byte, error)
	Close() error
}

type PacketConnDatagramPath struct {
	name        string
	conn        net.PacketConn
	idleTimeout time.Duration
	onClose     func()
	closeOnce   sync.Once
}

func NewPacketConnDatagramPath(name string, conn net.PacketConn, idleTimeout time.Duration, onClose func()) *PacketConnDatagramPath {
	if name == "" {
		name = "direct"
	}
	if idleTimeout <= 0 {
		idleTimeout = 2 * time.Second
	}
	return &PacketConnDatagramPath{name: name, conn: conn, idleTimeout: idleTimeout, onClose: onClose}
}

func (p *PacketConnDatagramPath) Name() string {
	if p == nil {
		return ""
	}
	return p.name
}

func (p *PacketConnDatagramPath) Send(ctx context.Context, payload []byte) error {
	if p == nil || p.conn == nil {
		return net.ErrClosed
	}
	if len(payload) > protocol.MaxUDPDatagramPayload {
		return errors.New("direct desktop datagram exceeds UDP payload limit")
	}
	deadline := time.Now().Add(p.idleTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := p.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	defer p.conn.SetWriteDeadline(time.Time{})
	_, err := p.conn.WriteTo(payload, nil)
	return err
}

func (p *PacketConnDatagramPath) Receive(ctx context.Context) ([]byte, error) {
	if p == nil || p.conn == nil {
		return nil, net.ErrClosed
	}
	deadline := time.Now().Add(p.idleTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := p.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	defer p.conn.SetReadDeadline(time.Time{})
	buffer := make([]byte, protocol.MaxUDPDatagramPayload)
	n, _, err := p.conn.ReadFrom(buffer)
	if err != nil {
		return nil, err
	}
	return buffer[:n], nil
}

func (p *PacketConnDatagramPath) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.closeOnce.Do(func() {
		if p.conn != nil {
			err = p.conn.Close()
		}
		if p.onClose != nil {
			p.onClose()
		}
	})
	return err
}
