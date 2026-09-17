package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

// QUICStreamAdapter adapts quic.Stream to TunnelStream
type QUICStreamAdapter struct {
	*quic.Stream
	session    *QUICSession
	writeMu    sync.Mutex
	deadlineMu sync.Mutex
	closed     atomic.Bool
}

func (s *QUICStreamAdapter) Read(p []byte) (int, error) {
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := s.Stream.Read(p)
	if s.closed.Load() {
		return n, net.ErrClosed
	}
	return n, err
}
func (s *QUICStreamAdapter) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := s.Stream.Write(p)
	if s.closed.Load() {
		return n, net.ErrClosed
	}
	return n, err
}

func (s *QUICStreamAdapter) CloseWrite() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// quic.Stream.Close() closes the write-direction and sends a FIN frame
	return s.Stream.Close()
}

func (s *QUICStreamAdapter) Close() error {
	s.deadlineMu.Lock()
	if s.closed.Swap(true) {
		s.deadlineMu.Unlock()
		return nil
	}
	// Wake a concurrent writer before serializing the graceful write FIN.
	// Bytes accepted by earlier successful writes remain queued for delivery.
	s.Stream.CancelRead(0)
	_ = s.Stream.SetWriteDeadline(time.Now())
	s.deadlineMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.Stream.Close()
}

func (s *QUICStreamAdapter) SetDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetDeadline(t)
}
func (s *QUICStreamAdapter) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetReadDeadline(t)
}
func (s *QUICStreamAdapter) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	defer s.deadlineMu.Unlock()
	if s.closed.Load() {
		return net.ErrClosed
	}
	return s.Stream.SetWriteDeadline(t)
}

// Abort forcibly resets both directions of the stream.
func (s *QUICStreamAdapter) Abort() {
	s.closed.Store(true)
	s.Stream.CancelRead(0)
	s.Stream.CancelWrite(0)
}

// QUICSession implements TunnelSession using quic-go
type QUICSession struct {
	conn          *quic.Conn
	datagrams     *datagramMux
	peerDatagrams atomic.Bool
}

// DefaultQUICConfig returns the transport profile used by RelayProxy. The
// receive windows are large enough to keep high-BDP links busy while the
// stream/connection caps still bound peer-controlled memory growth.
func DefaultQUICConfig() *quic.Config {
	return &quic.Config{
		MaxIdleTimeout:                 60 * time.Second,
		KeepAlivePeriod:                15 * time.Second,
		InitialStreamReceiveWindow:     4 << 20,
		MaxStreamReceiveWindow:         32 << 20,
		InitialConnectionReceiveWindow: 16 << 20,
		MaxConnectionReceiveWindow:     128 << 20,
		MaxIncomingStreams:             1024,
		MaxIncomingUniStreams:          64,
		EnableDatagrams:                true,
	}
}

// NewQUICSession wraps an established quic.Conn
func NewQUICSession(conn *quic.Conn) *QUICSession {
	s := &QUICSession{conn: conn}
	s.datagrams = newDatagramMux(s)
	return s
}

// DialQUIC connects to targetAddr using QUIC
func DialQUIC(ctx context.Context, targetAddr string, tlsConfig *tls.Config, quicConfig *quic.Config) (*QUICSession, error) {
	if tlsConfig == nil {
		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"relayproxy-quic"},
		}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if tlsConfig.MinVersion < tls.VersionTLS13 {
		tlsConfig.MinVersion = tls.VersionTLS13
	}
	if len(tlsConfig.NextProtos) == 0 {
		tlsConfig.NextProtos = []string{"relayproxy-quic"}
	}

	if quicConfig == nil {
		quicConfig = DefaultQUICConfig()
	} else {
		quicConfig = quicConfig.Clone()
	}
	quicConfig.EnableDatagrams = true

	conn, err := quic.DialAddr(ctx, targetAddr, tlsConfig, quicConfig)
	if err != nil {
		return nil, fmt.Errorf("quic dial failed: %w", err)
	}

	return NewQUICSession(conn), nil
}

func (s *QUICSession) OpenStream(ctx context.Context) (TunnelStream, error) {
	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &QUICStreamAdapter{Stream: stream, session: s}, nil
}

func (s *QUICSession) AcceptStream(ctx context.Context) (TunnelStream, error) {
	stream, err := s.conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &QUICStreamAdapter{Stream: stream, session: s}, nil
}

func (s *QUICSession) Transport() TransportType {
	return TransportQUIC
}

func (s *QUICSession) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *QUICSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *QUICSession) Close() error {
	err := s.conn.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "session closed")
	s.datagrams.close()
	s.datagrams.wg.Wait()
	return err
}

func (s *QUICSession) Done() <-chan struct{} {
	return s.conn.Context().Done()
}
