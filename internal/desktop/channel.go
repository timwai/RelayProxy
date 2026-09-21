package desktop

import (
	"context"
	"sync"

	"relayproxy/internal/tunnel"
)

// MediaConn owns one Relay Desktop native-datagram association and its reliable
// lifetime stream. Encoded media packets are opaque at this layer.
type MediaConn struct {
	channel   *tunnel.DatagramChannel
	stream    tunnel.TunnelStream
	closeOnce sync.Once
}

func NewMediaConn(channel *tunnel.DatagramChannel, stream tunnel.TunnelStream) *MediaConn {
	return &MediaConn{channel: channel, stream: stream}
}

func (c *MediaConn) AssociationID() uint64 {
	if c == nil || c.channel == nil {
		return 0
	}
	return c.channel.ID
}

func (c *MediaConn) Send(ctx context.Context, packet []byte) error {
	if c == nil || c.channel == nil {
		return tunnel.ErrDatagramsUnsupported
	}
	return c.channel.Send(ctx, packet)
}

func (c *MediaConn) Receive(ctx context.Context) ([]byte, error) {
	if c == nil || c.channel == nil {
		return nil, tunnel.ErrDatagramsUnsupported
	}
	return c.channel.Receive(ctx)
}

func (c *MediaConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		if c.channel != nil {
			err = c.channel.Close()
		}
		if c.stream != nil {
			if streamErr := c.stream.Close(); err == nil {
				err = streamErr
			}
		}
	})
	return err
}
