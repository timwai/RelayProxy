package divert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
)

const maxUDPPayload = 65507

// UDPResponder must inject a reply from key.Destination to key.Source. Writing
// back from a local redirect listener is insufficient. It must honor ctx and
// return promptly when the association/server closes. Payload is caller-owned.
type UDPResponder func(ctx context.Context, key FlowKey, payload []byte) error

type udpAssociation struct {
	server *Server
	route  *ClassifiedFlow
	ctx    context.Context
	cancel context.CancelFunc
	last   atomic.Int64

	mu        sync.Mutex
	writeMu   sync.Mutex
	started   bool
	closed    bool
	pc        net.PacketConn
	initErr   error
	ready     chan struct{}
	readyOnce sync.Once
	respond   UDPResponder
}

func (a *udpAssociation) touch(now time.Time) { a.last.Store(now.UnixNano()) }
func (a *udpAssociation) expired(now time.Time) bool {
	return now.Sub(time.Unix(0, a.last.Load())) >= a.server.opts.UDPIdleTimeout
}

// ForwardUDP preserves one tunnel association and decision for the original
// five-tuple. It serializes writes and continuously relays every reply until
// idle expiry, error, or Close. There is no goroutine or dial per datagram.
func (s *Server) ForwardUDP(ctx context.Context, route *ClassifiedFlow, payload []byte, respond UDPResponder) error {
	if route == nil || route.owner != s || route.key.Protocol != ProtoUDP || route.udp == nil {
		return errors.New("divert: invalid UDP classification")
	}
	if route.decision.Action != ActionProxy {
		return ErrNotProxyFlow
	}
	if len(payload) > maxUDPPayload {
		return fmt.Errorf("divert: UDP payload exceeds %d bytes", maxUDPPayload)
	}
	if respond == nil {
		return errors.New("divert: original-source UDP reply injector required")
	}
	a := route.udp
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.udp[route.key] != a {
		s.mu.Unlock()
		return ErrAssociationClosed
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		s.mu.Unlock()
		return ErrAssociationClosed
	}
	if !a.started {
		a.started, a.respond = true, respond
		s.wg.Add(1)
		go s.runUDPAssociation(a)
	}
	a.mu.Unlock()
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ctx.Done():
		// A failed dial publishes its error before closing the association.
		// Preserve that error even when cancellation wins the ready select.
	case <-a.ready:
	}
	a.mu.Lock()
	pc, err := a.pc, a.initErr
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if pc == nil {
		return ErrAssociationClosed
	}

	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.ctx.Err() != nil {
		return ErrAssociationClosed
	}
	if err := s.writeUDP(ctx, pc, route, payload); err != nil {
		route.traffic.Finish("failed", err)
		s.removeUDPAssociation(a)
		return err
	}
	a.touch(time.Now())
	return nil
}

func (s *Server) writeUDP(ctx context.Context, pc net.PacketConn, route *ClassifiedFlow, payload []byte) error {
	deadline := time.Now().Add(s.opts.UDPWriteTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := pc.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = pc.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	var n int
	var err error
	// A connected *net.UDPConn rejects WriteTo. PacketConn tunnel adapters may
	// instead implement only WriteTo; pass the actual peer rather than nil.
	if connected, ok := pc.(net.Conn); ok && connected.RemoteAddr() != nil {
		n, err = connected.Write(payload)
	} else {
		n, err = pc.WriteTo(payload, net.UDPAddrFromAddrPort(route.key.Destination))
	}
	route.traffic.AddUpload(n)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && n != len(payload) {
		return io.ErrShortWrite
	}
	return err
}

func (s *Server) runUDPAssociation(a *udpAssociation) {
	defer s.wg.Done()
	defer s.removeUDPAssociation(a)
	dialCtx, cancel := context.WithTimeout(a.ctx, s.opts.DialTimeout)
	var pc net.PacketConn
	var err error
	if a.route.decision.DatagramRequired {
		if dialer, ok := s.dialer.(proxy.UDPOptionsDialer); ok {
			pc, err = dialer.DialUDPWithOptions(dialCtx, a.route.decision.ExitID, a.route.flow.IP, a.route.flow.Port, proxy.UDPDialOptions{DatagramRequired: true})
		} else {
			err = protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "divert: native UDP datagrams are required but the dialer does not support required datagram options")
		}
	} else {
		pc, err = s.dialer.DialUDP(dialCtx, a.route.decision.ExitID, a.route.flow.IP, a.route.flow.Port)
	}
	cancel()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		if pc != nil {
			_ = pc.Close()
		}
		return
	}
	if err == nil && pc == nil {
		err = errors.New("divert: UDP dialer returned a nil connection")
	}
	a.pc, a.initErr = pc, err
	a.readyOnce.Do(func() { close(a.ready) })
	a.mu.Unlock()
	if err != nil {
		a.route.traffic.Finish("failed", err)
		return
	}
	a.route.traffic.Activate()

	buf := make([]byte, maxUDPPayload)
	readDeadline := time.Now().Add(s.opts.UDPIdleTimeout)
	if err := pc.SetReadDeadline(readDeadline); err != nil {
		return
	}
	nextDeadlineRefresh := time.Now().Add(time.Second)
	for {
		if a.ctx.Err() != nil {
			return
		}
		n, _, err := pc.ReadFrom(buf)
		a.route.traffic.AddDownload(n)
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() && !a.expired(time.Now()) {
				readDeadline = time.Now().Add(s.opts.UDPIdleTimeout)
				if err := pc.SetReadDeadline(readDeadline); err != nil {
					return
				}
				nextDeadlineRefresh = time.Now().Add(time.Second)
				continue
			}
			return
		}
		now := time.Now()
		a.touch(now)
		if !now.Before(nextDeadlineRefresh) {
			readDeadline = now.Add(s.opts.UDPIdleTimeout)
			if err := pc.SetReadDeadline(readDeadline); err != nil {
				return
			}
			nextDeadlineRefresh = now.Add(time.Second)
		}
		if err := a.respond(a.ctx, a.route.key, buf[:n]); err != nil {
			return
		}
	}
}

func (a *udpAssociation) close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	pc := a.pc
	a.pc = nil
	if a.initErr == nil {
		a.initErr = ErrAssociationClosed
	}
	a.readyOnce.Do(func() { close(a.ready) })
	a.mu.Unlock()
	a.cancel()
	a.route.traffic.Finish("closed", nil)
	if pc != nil {
		_ = pc.Close()
	}
}

func (s *Server) removeUDPAssociation(a *udpAssociation) {
	s.mu.Lock()
	if s.udp[a.route.key] == a {
		delete(s.udp, a.route.key)
	}
	s.mu.Unlock()
	a.close()
}

func (s *Server) pruneUDPLocked(now time.Time) []*udpAssociation {
	var expired []*udpAssociation
	for key, association := range s.udp {
		if association.expired(now) {
			delete(s.udp, key)
			expired = append(expired, association)
		}
	}
	return expired
}

func (s *Server) startUDPSweeperLocked() {
	if s.sweeping {
		return
	}
	s.sweeping = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		interval := min(s.opts.UDPIdleTimeout/2, time.Second)
		if interval < time.Millisecond {
			interval = time.Millisecond
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case now := <-ticker.C:
				s.mu.Lock()
				expired := s.pruneUDPLocked(now)
				s.mu.Unlock()
				for _, association := range expired {
					association.close()
				}
			}
		}
	}()
}

func (s *Server) UDPAssociationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.udp)
}
