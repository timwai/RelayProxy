package desktop

import (
	"context"
	"errors"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// MediaConn owns one Relay Desktop native-datagram association and its reliable
// lifetime stream. Encoded media packets are opaque at this layer.
type MediaConn struct {
	channel       *tunnel.DatagramChannel
	stream        tunnel.TunnelStream
	closeOnce     sync.Once
	readMu        sync.Mutex
	writeMu       sync.Mutex
	datagramWrite sync.Mutex

	pathMu sync.RWMutex
	direct DatagramPath
}

func NewMediaConn(channel *tunnel.DatagramChannel, stream tunnel.TunnelStream) *MediaConn {
	return &MediaConn{channel: channel, stream: stream}
}

func (c *MediaConn) SetDatagramPath(path DatagramPath) {
	if c == nil {
		if path != nil {
			_ = path.Close()
		}
		return
	}
	c.pathMu.Lock()
	old := c.direct
	c.direct = path
	c.pathMu.Unlock()
	if old != nil && old != path {
		_ = old.Close()
	}
}

func (c *MediaConn) ClearDatagramPath(path DatagramPath) {
	if c == nil {
		return
	}
	c.pathMu.Lock()
	if path != nil && c.direct != path {
		c.pathMu.Unlock()
		return
	}
	old := c.direct
	c.direct = nil
	c.pathMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func (c *MediaConn) DatagramPathName() string {
	if c == nil {
		return ""
	}
	c.pathMu.RLock()
	path := c.direct
	c.pathMu.RUnlock()
	if path == nil {
		return "relay"
	}
	if name := path.Name(); name != "" {
		return name
	}
	return "direct"
}

func (c *MediaConn) directPath() DatagramPath {
	if c == nil {
		return nil
	}
	c.pathMu.RLock()
	defer c.pathMu.RUnlock()
	return c.direct
}

func (c *MediaConn) failDirectPath(path DatagramPath) {
	if c == nil || path == nil {
		return
	}
	c.pathMu.Lock()
	if c.direct != path {
		c.pathMu.Unlock()
		return
	}
	c.direct = nil
	c.pathMu.Unlock()
	_ = path.Close()
}

func (c *MediaConn) AssociationID() uint64 {
	if c == nil || c.channel == nil {
		return 0
	}
	return c.channel.ID
}

func (c *MediaConn) Send(ctx context.Context, packet []byte) error {
	if c == nil {
		return tunnel.ErrDatagramsUnsupported
	}
	c.datagramWrite.Lock()
	defer c.datagramWrite.Unlock()
	if path := c.directPath(); path != nil {
		if err := path.Send(ctx, packet); err == nil {
			return nil
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			c.failDirectPath(path)
		}
	}
	if c.channel == nil {
		return tunnel.ErrDatagramsUnsupported
	}
	return c.channel.Send(ctx, packet)
}

func (c *MediaConn) Receive(ctx context.Context) ([]byte, error) {
	if c == nil {
		return nil, tunnel.ErrDatagramsUnsupported
	}
	if path := c.directPath(); path != nil {
		packet, err := path.Receive(ctx)
		if err == nil {
			return packet, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c.failDirectPath(path)
	}
	if c.channel == nil {
		return nil, tunnel.ErrDatagramsUnsupported
	}
	return c.channel.Receive(ctx)
}

func (c *MediaConn) SendSessionMessage(ctx context.Context, message protocol.DesktopSessionMessage) error {
	if c == nil || c.stream == nil {
		return errors.New("Relay Desktop reliable channel is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.stream.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer c.stream.SetWriteDeadline(time.Time{})
	}
	return protocol.WriteJSON(c.stream, message)
}

func (c *MediaConn) ReceiveSessionMessage(ctx context.Context) (protocol.DesktopSessionMessage, error) {
	if c == nil || c.stream == nil {
		return protocol.DesktopSessionMessage{}, errors.New("Relay Desktop reliable channel is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return protocol.DesktopSessionMessage{}, err
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		if err := c.stream.SetReadDeadline(deadline); err != nil {
			return protocol.DesktopSessionMessage{}, err
		}
		defer c.stream.SetReadDeadline(time.Time{})
	}
	var message protocol.DesktopSessionMessage
	if err := protocol.ReadJSON(c.stream, &message); err != nil {
		return protocol.DesktopSessionMessage{}, err
	}
	return message, nil
}

func (c *MediaConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.pathMu.Lock()
		direct := c.direct
		c.direct = nil
		c.pathMu.Unlock()
		if direct != nil {
			err = direct.Close()
		}
		if c.channel != nil {
			if channelErr := c.channel.Close(); err == nil {
				err = channelErr
			}
		}
		if c.stream != nil {
			if streamErr := c.stream.Close(); err == nil {
				err = streamErr
			}
		}
	})
	return err
}
