package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	p2presume "relayproxy/internal/p2p/resume"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const resumeRecoveryTimeout = 10 * time.Second

type resumableTCPConn struct {
	dialer   *TunnelDialer
	endpoint *p2presume.Endpoint
	state    *p2presume.StreamState
	exitID   string
	host     string
	port     uint16

	localAddr  net.Addr
	remoteAddr net.Addr

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func newResumableTCPConn(
	dialer *TunnelDialer,
	endpoint *p2presume.Endpoint,
	state *p2presume.StreamState,
	exitID, host string,
	port uint16,
	localAddr, remoteAddr net.Addr,
) net.Conn {
	ctx, cancel := context.WithCancel(context.Background())
	conn := &resumableTCPConn{
		dialer: dialer, endpoint: endpoint, state: state,
		exitID: exitID, host: host, port: port,
		localAddr: localAddr, remoteAddr: remoteAddr,
		ctx: ctx, cancel: cancel,
	}
	go conn.recoveryLoop()
	return conn
}

func (c *resumableTCPConn) Read(p []byte) (int, error) {
	if c == nil || c.endpoint == nil {
		return 0, net.ErrClosed
	}
	return c.endpoint.Read(p)
}

func (c *resumableTCPConn) Write(p []byte) (int, error) {
	if c == nil || c.endpoint == nil {
		return 0, net.ErrClosed
	}
	return c.endpoint.Write(p)
}

func (c *resumableTCPConn) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.cancel()
		err = c.endpoint.Close()
	})
	return err
}

func (c *resumableTCPConn) CloseWrite() error {
	if c == nil || c.endpoint == nil {
		return net.ErrClosed
	}
	return c.endpoint.CloseWrite()
}

func (c *resumableTCPConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *resumableTCPConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *resumableTCPConn) SetDeadline(t time.Time) error {
	return c.endpoint.SetDeadline(t)
}

func (c *resumableTCPConn) SetReadDeadline(t time.Time) error {
	return c.endpoint.SetReadDeadline(t)
}

func (c *resumableTCPConn) SetWriteDeadline(t time.Time) error {
	return c.endpoint.SetWriteDeadline(t)
}

func (c *resumableTCPConn) recoveryLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case loss := <-c.endpoint.Losses():
			if c.ctx.Err() != nil || c.endpoint.Closed() {
				return
			}
			if loss.Generation != c.endpoint.Generation() {
				continue
			}
			if !c.dialer.directFallbackEnabled() {
				c.endpoint.Abort(fmt.Errorf("resumable TCP transport lost with Relay fallback disabled: %w", loss.Err))
				return
			}
			if err := c.recoverViaRelay(loss.Generation); err != nil {
				c.endpoint.Abort(err)
				return
			}
		}
	}
}

func (c *resumableTCPConn) recoverViaRelay(lostGeneration uint64) error {
	ctx, cancel := context.WithTimeout(c.ctx, resumeRecoveryTimeout)
	defer cancel()
	nextGeneration := lostGeneration + 1
	backoff := 100 * time.Millisecond
	var lastErr error

	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("resumable TCP recovery timed out: %w", lastErr)
			}
			return err
		}
		if c.endpoint.Generation() != lostGeneration {
			return nil
		}

		var relay tunnel.TunnelSession
		if c.dialer.getTunnel != nil {
			relay = c.dialer.getTunnel()
		}
		if relay != nil {
			if err := c.tryRelayRebind(ctx, relay, lostGeneration, nextGeneration); err == nil {
				c.dialer.recordFallback(c.exitID)
				return nil
			} else {
				lastErr = err
				var relayErr *protocol.RelayError
				if errors.As(err, &relayErr) {
					return err
				}
			}
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if backoff < time.Second {
			backoff *= 2
			if backoff > time.Second {
				backoff = time.Second
			}
		}
	}
}

func (c *resumableTCPConn) tryRelayRebind(
	ctx context.Context,
	relay tunnel.TunnelSession,
	currentGeneration, nextGeneration uint64,
) error {
	if relay == nil {
		return errors.New("Relay session is unavailable")
	}
	stream, err := relay.OpenStream(ctx)
	if err != nil {
		return err
	}
	owned := true
	defer func() {
		if owned {
			_ = stream.Close()
		}
	}()
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()

	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = stream.SetDeadline(deadline)

	current, err := c.state.Binding(p2presume.BindAck, currentGeneration)
	if err != nil {
		return err
	}
	requestBinding, err := c.state.Binding(p2presume.BindOpen, nextGeneration)
	if err != nil {
		return err
	}
	wire, err := p2presume.BindingToProtocol(requestBinding, protocol.TCPResumeModeRebind)
	if err != nil {
		return err
	}

	reqID := c.dialer.nextRequestID()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenTCP,
		RequestID: reqID, ExitDeviceID: c.exitID,
	}); err != nil {
		return err
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPRequest{
		RequestID: reqID,
		Host:      c.host,
		Port:      c.port,
		TimeoutMs: 5000,
		Resume:    wire,
	}); err != nil {
		return err
	}

	var resp protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &resp); err != nil {
		return err
	}
	if !resp.Success {
		code := resp.ErrorCode
		if code == "" {
			code = protocol.ErrCodeStreamOpenFailed
		}
		return protocol.NewRelayError(code, resp.ErrorMessage)
	}
	if resp.Resume == nil {
		return errors.New("Exit did not acknowledge resumable TCP rebind")
	}
	peer, err := p2presume.ResponseBindingFromProtocol(resp.Resume)
	if err != nil || peer.Generation != nextGeneration {
		return p2presume.ErrBinding
	}
	if err := p2presume.ValidateRebind(current, peer); err != nil {
		return err
	}

	_ = stream.SetDeadline(time.Time{})
	stopCancel()
	if err := c.endpoint.Bind(stream, nextGeneration); err != nil {
		return err
	}
	owned = false
	return nil
}
