package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/quic-go"
	quicprofile "relayproxy/internal/congestion"
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
	if s.session != nil {
		s.session.activeStreams.Add(-1)
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
	if s.closed.Swap(true) {
		return
	}
	if s.session != nil {
		s.session.activeStreams.Add(-1)
	}
	s.Stream.CancelRead(0)
	s.Stream.CancelWrite(0)
}

// QUICSession implements TunnelSession using quic-go
type QUICSession struct {
	conn             *quic.Conn
	datagrams        *datagramMux
	peerDatagrams    atomic.Bool
	peerStreamResume atomic.Bool
	activeStreams    atomic.Int64
	ownedPacketConn  net.PacketConn
	closeOnce        sync.Once
	closeErr         error

	diagnosticsMu            sync.Mutex
	diagnosticsLastAt        time.Time
	diagnosticsLastBytesSent uint64
	diagnosticsLastBytesRecv uint64
	diagnosticsSendBPS       uint64
	diagnosticsRecvBPS       uint64
	udpReadBufferBytes       int
	udpWriteBufferBytes      int
	congestionController     string
	congestionTargetBPS      uint64
}

// DefaultQUICConfig returns the transport profile used by RelayProxy. The
// receive windows are large enough to keep high-BDP links busy while the
// stream/connection caps still bound peer-controlled memory growth.
func DefaultQUICConfig() *quic.Config {
	return &quic.Config{
		// RelayProxy has an authenticated application-layer Ping/Pong heartbeat.
		// Keep transport-level keepalive disabled to avoid duplicate idle packets
		// and radio wakeups, especially on mobile exits.
		MaxIdleTimeout:                 120 * time.Second,
		KeepAlivePeriod:                0,
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
	// All RelayProxy QUIC paths share Hysteria's aggressive BBR congestion controller.
	// Installing it here covers Relay, P2P and Public Direct without duplicating
	// transport-specific tuning at every dial / accept site.
	quicprofile.UseDefaultBBR(conn)

	s := &QUICSession{conn: conn, congestionController: "bbr-aggressive"}
	s.datagrams = newDatagramMux(s)
	return s
}

// UseBrutal switches an established QUIC session to Hysteria Brutal.
// QUIC starts on BBR Aggressive so authentication and capability negotiation
// don't depend on a bandwidth hint. The authenticated control plane can then
// opt into Brutal for the sending direction.
func (s *QUICSession) UseBrutal(txBPS uint64, disableLossCompensation bool) bool {
	if s == nil || s.conn == nil || txBPS == 0 {
		return false
	}
	txBPS = quicprofile.CapRequestedRate(txBPS, 0)
	if txBPS == 0 {
		return false
	}
	quicprofile.UseBrutal(s.conn, txBPS, disableLossCompensation)
	s.diagnosticsMu.Lock()
	s.congestionController = "brutal"
	s.congestionTargetBPS = txBPS
	s.diagnosticsMu.Unlock()
	return true
}

// UseBrutal configures a TunnelSession when its transport is QUIC.
// TLS/yamux sessions intentionally ignore QUIC congestion settings.
func UseBrutal(session TunnelSession, txBPS uint64, disableLossCompensation bool) bool {
	quicSession, ok := session.(*QUICSession)
	return ok && quicSession.UseBrutal(txBPS, disableLossCompensation)
}

func newOwnedQUICSession(conn *quic.Conn, packetConn net.PacketConn) *QUICSession {
	s := NewQUICSession(conn)
	s.ownedPacketConn = packetConn
	if udpConn, ok := packetConn.(*net.UDPConn); ok {
		s.setUDPSocketBufferSizes(udpConn)
	}
	return s
}

func (s *QUICSession) setUDPSocketBufferSizes(conn *net.UDPConn) {
	if s == nil || conn == nil {
		return
	}
	s.udpReadBufferBytes, s.udpWriteBufferBytes = udpSocketBufferSizes(conn)
}

func (s *QUICSession) sampleByteRates(now time.Time, bytesSent, bytesRecv uint64) (sendBPS, recvBPS uint64) {
	if s == nil {
		return 0, 0
	}
	s.diagnosticsMu.Lock()
	defer s.diagnosticsMu.Unlock()

	if s.diagnosticsLastAt.IsZero() {
		s.diagnosticsLastAt = now
		s.diagnosticsLastBytesSent = bytesSent
		s.diagnosticsLastBytesRecv = bytesRecv
		return 0, 0
	}
	elapsed := now.Sub(s.diagnosticsLastAt)
	if elapsed < 250*time.Millisecond {
		return s.diagnosticsSendBPS, s.diagnosticsRecvBPS
	}
	seconds := elapsed.Seconds()
	if bytesSent >= s.diagnosticsLastBytesSent {
		s.diagnosticsSendBPS = uint64(float64(bytesSent-s.diagnosticsLastBytesSent) / seconds)
	} else {
		s.diagnosticsSendBPS = 0
	}
	if bytesRecv >= s.diagnosticsLastBytesRecv {
		s.diagnosticsRecvBPS = uint64(float64(bytesRecv-s.diagnosticsLastBytesRecv) / seconds)
	} else {
		s.diagnosticsRecvBPS = 0
	}
	s.diagnosticsLastAt = now
	s.diagnosticsLastBytesSent = bytesSent
	s.diagnosticsLastBytesRecv = bytesRecv
	return s.diagnosticsSendBPS, s.diagnosticsRecvBPS
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
	s.activeStreams.Add(1)
	return &QUICStreamAdapter{Stream: stream, session: s}, nil
}

func (s *QUICSession) AcceptStream(ctx context.Context) (TunnelStream, error) {
	stream, err := s.conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	s.activeStreams.Add(1)
	return &QUICStreamAdapter{Stream: stream, session: s}, nil
}

func (s *QUICSession) ActiveStreams() int64 {
	if s == nil {
		return 0
	}
	return s.activeStreams.Load()
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
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.conn.CloseWithError(quic.ApplicationErrorCode(quic.NoError), "session closed")
		s.datagrams.close()
		s.datagrams.wg.Wait()
		if s.ownedPacketConn != nil {
			if err := s.ownedPacketConn.Close(); s.closeErr == nil {
				s.closeErr = err
			}
		}
	})
	return s.closeErr
}

func (s *QUICSession) Done() <-chan struct{} {
	return s.conn.Context().Done()
}
