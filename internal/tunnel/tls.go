package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
)

// YAMUXStreamAdapter adapts yamux.Stream to TunnelStream
type YAMUXStreamAdapter struct {
	*yamux.Stream
	deadlineMu sync.Mutex
	closed     atomic.Bool
}

func (s *YAMUXStreamAdapter) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := s.Stream.Read(p)
	if s.closed.Load() {
		return n, net.ErrClosed
	}
	return n, err
}

func (s *YAMUXStreamAdapter) Write(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := s.Stream.Write(p)
	if s.closed.Load() {
		return n, net.ErrClosed
	}
	return n, err
}

// yamux Close is a write half-close. A full close must also wake local readers.
func (s *YAMUXStreamAdapter) Close() error {
	s.deadlineMu.Lock()
	if s.closed.Swap(true) {
		s.deadlineMu.Unlock()
		return nil
	}
	_ = s.Stream.SetDeadline(time.Now())
	s.deadlineMu.Unlock()
	return s.Stream.Close()
}

func (s *YAMUXStreamAdapter) SetDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetDeadline(t)
}
func (s *YAMUXStreamAdapter) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetReadDeadline(t)
}
func (s *YAMUXStreamAdapter) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetWriteDeadline(t)
}

func (s *YAMUXStreamAdapter) CloseWrite() error {
	// yamux.Stream.Close() sends a FIN flag to indicate write half-close
	return s.Stream.Close()
}

const (
	maxConcurrentYAMUXOpens = 16
	yamuxAcceptBacklog      = 1024
)

// TLSSession implements TunnelSession using TLS + yamux multiplexer
type TLSSession struct {
	conn     net.Conn
	session  *yamux.Session
	openGate chan struct{}
	closed   atomic.Bool
}

// DefaultYAMUXConfig keeps stream setup responsive and permits enough
// in-flight data for high-latency links without using yamux's tiny 256 KiB
// stream window.
func DefaultYAMUXConfig() *yamux.Config {
	config := yamux.DefaultConfig()
	// Match the Relay's default per-device stream ceiling. yamux assumes a
	// symmetric backlog and blocks outgoing SYNs when this queue fills; keeping
	// the library default (256) would impose an unintended lower burst limit.
	config.AcceptBacklog = yamuxAcceptBacklog
	config.MaxStreamWindowSize = 16 << 20
	config.StreamOpenTimeout = 15 * time.Second
	config.StreamCloseTimeout = 30 * time.Second
	config.EnableKeepAlive = true
	config.KeepAliveInterval = 15 * time.Second
	return config
}

// NewTLSSession wraps an established TLS connection and yamux session
func NewTLSSession(conn net.Conn, session *yamux.Session) *TLSSession {
	return &TLSSession{
		conn:     conn,
		session:  session,
		openGate: make(chan struct{}, maxConcurrentYAMUXOpens),
	}
}

// DialTLS connects to targetAddr using TLS and initializes a yamux client session
func DialTLS(ctx context.Context, targetAddr string, tlsConfig *tls.Config, yamuxConfig *yamux.Config) (*TLSSession, error) {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
	}
	rawConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return nil, fmt.Errorf("tcp dial failed: %w", err)
	}
	TuneTCPConn(rawConn)

	var sessionConn net.Conn = rawConn
	if tlsConfig != nil {
		tlsConfig = tlsConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS13 {
			tlsConfig.MinVersion = tls.VersionTLS13
		}
		if tlsConfig.ServerName == "" {
			host, _, err := net.SplitHostPort(targetAddr)
			if err == nil {
				tlsConfig.ServerName = host
			}
		}

		tlsConn := tls.Client(rawConn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			rawConn.Close()
			return nil, fmt.Errorf("tls handshake failed: %w", err)
		}
		sessionConn = tlsConn
	}

	if yamuxConfig == nil {
		yamuxConfig = DefaultYAMUXConfig()
	}

	session, err := yamux.Client(sessionConn, yamuxConfig)
	if err != nil {
		sessionConn.Close()
		return nil, fmt.Errorf("yamux client init failed: %w", err)
	}

	return NewTLSSession(sessionConn, session), nil
}

// ServerTLS wraps an incoming TLS net.Conn into a yamux server session
func ServerTLS(tlsConn net.Conn, yamuxConfig *yamux.Config) (*TLSSession, error) {
	if yamuxConfig == nil {
		yamuxConfig = DefaultYAMUXConfig()
	}

	session, err := yamux.Server(tlsConn, yamuxConfig)
	if err != nil {
		return nil, fmt.Errorf("yamux server init failed: %w", err)
	}

	return NewTLSSession(tlsConn, session), nil
}

func (s *TLSSession) OpenStream(ctx context.Context) (TunnelStream, error) {
	if s.closed.Load() {
		return nil, net.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// yamux has no cancellable OpenStream. Bound concurrent underlying opens so
	// cancelled callers cannot accumulate unbounded workers while browser-style
	// connection bursts can still establish in parallel.
	select {
	case s.openGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.Done():
		return nil, net.ErrClosed
	}

	releaseGate := func() { <-s.openGate }
	if err := ctx.Err(); err != nil {
		releaseGate()
		return nil, err
	}
	if s.closed.Load() {
		releaseGate()
		return nil, net.ErrClosed
	}
	select {
	case <-s.Done():
		releaseGate()
		return nil, net.ErrClosed
	default:
	}

	// Background/TODO contexts cannot be cancelled by the caller. Avoid a
	// wrapper goroutine and result channel on this common internal fast path.
	// The gate still bounds yamux opens and Close waits for all occupied slots.
	if ctx.Done() == nil {
		stream, err := s.session.OpenStream()
		releaseGate()
		if err != nil {
			return nil, err
		}
		if s.closed.Load() {
			_ = (&YAMUXStreamAdapter{Stream: stream}).Close()
			return nil, net.ErrClosed
		}
		return &YAMUXStreamAdapter{Stream: stream}, nil
	}

	type result struct {
		stream *yamux.Stream
		err    error
	}
	ch := make(chan result)
	go func() {
		defer releaseGate()
		stream, err := s.session.OpenStream()
		select {
		case ch <- result{stream: stream, err: err}:
			return
		case <-ctx.Done():
		case <-s.Done():
		}
		if stream != nil {
			_ = (&YAMUXStreamAdapter{Stream: stream}).Close()
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.Done():
		return nil, net.ErrClosed
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if err := ctx.Err(); err != nil {
			_ = (&YAMUXStreamAdapter{Stream: r.stream}).Close()
			return nil, err
		}
		return &YAMUXStreamAdapter{Stream: r.stream}, nil
	}
}

func (s *TLSSession) AcceptStream(ctx context.Context) (TunnelStream, error) {
	stream, err := s.session.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return &YAMUXStreamAdapter{Stream: stream}, nil
}

func (s *TLSSession) Transport() TransportType {
	return TransportTLS
}

func (s *TLSSession) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *TLSSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *TLSSession) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	_ = s.session.Close()
	err := s.conn.Close()
	// Drain the full semaphore capacity. Once the yamux session is closed,
	// new OpenStream callers fail quickly, so acquiring every slot forms a
	// barrier for all in-flight OpenStream workers without serializing normal
	// stream creation to a single worker.
	for i := 0; i < cap(s.openGate); i++ {
		s.openGate <- struct{}{}
	}
	for i := 0; i < cap(s.openGate); i++ {
		<-s.openGate
	}
	return err
}

func (s *TLSSession) Done() <-chan struct{} {
	return s.session.CloseChan()
}
