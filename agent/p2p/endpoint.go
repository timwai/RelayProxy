package p2p

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// Endpoint owns the UDP socket that must be reused across reflexive discovery,
// hole punching and the future QUIC transport. Replacing this socket changes
// the NAT mapping and would invalidate the candidates sent through the server.
type Endpoint struct {
	mu         sync.RWMutex
	rendezvous string
	conn       *net.UDPConn
	identity   *secure.TLSIdentity
	candidates []protocol.P2PCandidate
	closed     bool
}

func NewEndpoint(rendezvous string) *Endpoint {
	return &Endpoint{rendezvous: rendezvous}
}

func (e *Endpoint) Start(ctx context.Context) error {
	if e == nil {
		return errors.New("nil P2P endpoint")
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return net.ErrClosed
	}
	if e.conn != nil {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return err
	}
	tunnel.TuneUDPConn(conn)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	discovered := filterIPv4UDPCandidates(candidate.Discover(port, 0))
	if e.rendezvous != "" {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		reflexive, probeErr := candidate.ProbeReflexive(probeCtx, e.rendezvous, conn, "udp")
		cancel()
		if probeErr == nil {
			discovered = append(discovered, reflexive)
		}
	}
	if validated, validateErr := candidate.Validate(discovered); validateErr == nil {
		discovered = filterIPv4UDPCandidates(validated)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		_ = conn.Close()
		return net.ErrClosed
	}
	if e.conn != nil {
		e.mu.Unlock()
		_ = conn.Close()
		return nil
	}
	e.conn = conn
	e.identity = identity
	e.candidates = append([]protocol.P2PCandidate(nil), discovered...)
	e.mu.Unlock()
	return nil
}

func (e *Endpoint) Description() ([]protocol.P2PCandidate, string, error) {
	if e == nil {
		return nil, "", errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, "", net.ErrClosed
	}
	if e.conn == nil || e.identity == nil {
		return nil, "", errors.New("P2P endpoint is not started")
	}
	if len(e.candidates) == 0 {
		return nil, "", errors.New("P2P endpoint has no usable UDP candidate")
	}
	return append([]protocol.P2PCandidate(nil), e.candidates...), e.identity.Fingerprint, nil
}

func (e *Endpoint) UDPConn() (*net.UDPConn, error) {
	if e == nil {
		return nil, errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.conn == nil {
		return nil, net.ErrClosed
	}
	return e.conn, nil
}

func (e *Endpoint) Identity() (*secure.TLSIdentity, error) {
	if e == nil {
		return nil, errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.identity == nil {
		return nil, net.ErrClosed
	}
	return e.identity, nil
}

func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	conn := e.conn
	e.conn = nil
	e.identity = nil
	e.candidates = nil
	e.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func filterIPv4UDPCandidates(input []protocol.P2PCandidate) []protocol.P2PCandidate {
	result := make([]protocol.P2PCandidate, 0, len(input))
	for _, item := range input {
		if item.Protocol != "udp" {
			continue
		}
		address, err := netip.ParseAddrPort(item.Address)
		if err != nil || !address.Addr().Unmap().Is4() {
			continue
		}
		result = append(result, item)
	}
	return result
}
