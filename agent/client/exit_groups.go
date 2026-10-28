package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"relayproxy/internal/exitgroup"
	"relayproxy/internal/protocol"
)

type exitGroups struct {
	mu     sync.RWMutex
	groups []exitgroup.Group
	usable func(string) bool
}

// ConfigureExitGroups publishes a defensive snapshot. Eligibility is consulted
// for each new connection; an established stream/UDP association never moves.
func (d *TunnelDialer) ConfigureExitGroups(groups []exitgroup.Group, usable func(string) bool) {
	d.groups.mu.Lock()
	defer d.groups.mu.Unlock()
	d.groups.groups, d.groups.usable = exitgroup.Clone(groups), usable
}

func (d *TunnelDialer) groupCandidates(id string) []string {
	d.groups.mu.RLock()
	groups, usable := d.groups.groups, d.groups.usable
	d.groups.mu.RUnlock()
	return exitgroup.Candidates(groups, id, usable)
}

// SelectedExit identifies the concrete Agent without losing transport metrics.
type selectedExitConn struct {
	net.Conn
	id string
}

func (c *selectedExitConn) SelectedExitID() string { return c.id }
func (c *selectedExitConn) ProxyPath() string {
	if p, ok := c.Conn.(interface{ ProxyPath() string }); ok {
		return p.ProxyPath()
	}
	return ""
}
func (c *selectedExitConn) CloseWrite() error {
	if p, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return p.CloseWrite()
	}
	return nil
}

type selectedExitPacketConn struct {
	net.PacketConn
	id string
}

func (c *selectedExitPacketConn) SelectedExitID() string { return c.id }
func (c *selectedExitPacketConn) ProxyPath() string {
	if p, ok := c.PacketConn.(interface{ ProxyPath() string }); ok {
		return p.ProxyPath()
	}
	return ""
}

func terminalGroupError(err error) bool {
	var relay *protocol.RelayError
	// Permission failures must remain permission failures, never trigger an
	// implicit direct connection or a retry outside the configured group.
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.As(err, &relay) {
		switch relay.Code {
		case protocol.ErrCodeAuthFailed, protocol.ErrCodeAccessDenied, protocol.ErrCodeACLDenied, protocol.ErrCodeInvalidRequest:
			return true
		}
	}
	return false
}

func (d *TunnelDialer) DialTCP(ctx context.Context, id, host string, port uint16) (net.Conn, error) {
	if id == "" {
		id = d.GetDefaultExitID()
	}
	if !exitgroup.IsID(id) {
		return d.dialTCP(ctx, id, host, port)
	}
	var failures []error
	for _, member := range d.groupCandidates(id) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := d.dialTCP(attempt, member, host, port)
		cancel()
		if err == nil && conn != nil {
			return &selectedExitConn{Conn: conn, id: member}, nil
		}
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil {
			err = errors.New("nil TCP connection")
		}
		failures = append(failures, fmt.Errorf("%s: %w", member, err))
		if terminalGroupError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("设备分组 %s 没有可用出口: %w", id, errors.Join(append([]error{errors.New("all group members unavailable")}, failures...)...))
}

func (d *TunnelDialer) DialUDPWithOptions(ctx context.Context, id, host string, port uint16, opts UDPDialOptions) (net.PacketConn, error) {
	if id == "" {
		id = d.GetDefaultExitID()
	}
	if !exitgroup.IsID(id) {
		return d.dialUDPWithOptions(ctx, id, host, port, opts)
	}
	var failures []error
	for _, member := range d.groupCandidates(id) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := d.dialUDPWithOptions(attempt, member, host, port, opts)
		cancel()
		if err == nil && conn != nil {
			return &selectedExitPacketConn{PacketConn: conn, id: member}, nil
		}
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil {
			err = errors.New("nil UDP association")
		}
		failures = append(failures, fmt.Errorf("%s: %w", member, err))
		if terminalGroupError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("设备分组 %s 没有可用 UDP 出口: %w", id, errors.Join(append([]error{errors.New("all group members unavailable")}, failures...)...))
}

func (c *selectedExitConn) CloseRead() error {
	if p, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return p.CloseRead()
	}
	return nil
}
func (c *selectedExitPacketConn) UDPMode() string {
	if p, ok := c.PacketConn.(interface{ UDPMode() string }); ok {
		return p.UDPMode()
	}
	return ""
}
