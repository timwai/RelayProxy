package tunnel

import (
	"sync"
	"time"
)

// DialTimings uses milliseconds. TCP's ConnectMS includes DNS and the TCP
// handshake; QUIC reports DNS separately and HandshakeMS includes address racing.
type DialTimings struct {
	DNSMS        int64 `json:"dnsMs,omitempty"`
	ConnectMS    int64 `json:"connectMs,omitempty"`
	HandshakeMS  int64 `json:"handshakeMs"`
	TotalMS      int64 `json:"totalMs"`
	AddressCount int   `json:"addressCount,omitempty"`
}

type ConnectionMetrics struct {
	FirstByteMS          int64         `json:"firstByteMs"`
	Attempts             uint64        `json:"attempts"`
	Failures             uint64        `json:"failures"`
	Reconnects           uint64        `json:"reconnects"`
	NetworkChanges       uint64        `json:"networkChanges"`
	LastConnectMS        int64         `json:"lastConnectMs"`
	LastRecoveryMS       int64         `json:"lastRecoveryMs"`
	ControlReadyMS       int64         `json:"controlReadyMs"`
	Transport            TransportType `json:"transport,omitempty"`
	FallbackReason       string        `json:"fallbackReason,omitempty"`
	LastError            string        `json:"lastError,omitempty"`
	QUICRecoveryAttempts uint64        `json:"quicRecoveryAttempts"`
	QUICRecoveries       uint64        `json:"quicRecoveries"`
	QUICRecoveryError    string        `json:"quicRecoveryError,omitempty"`
	Dial                 *DialTimings  `json:"dial,omitempty"`
}

type measuredControlStream struct {
	TunnelStream
	manager *TunnelManager
	session TunnelSession
	once    sync.Once
}

func (s *measuredControlStream) Read(p []byte) (int, error) {
	n, err := s.TunnelStream.Read(p)
	if n > 0 {
		s.once.Do(func() {
			s.manager.mu.Lock()
			defer s.manager.mu.Unlock()
			if s.manager.session == s.session && !s.manager.connectedAt.IsZero() {
				s.manager.metrics.FirstByteMS = time.Since(s.manager.connectedAt).Milliseconds()
			}
		})
	}
	return n, err
}

func (m *TunnelManager) TrackControlStream(sess TunnelSession, stream TunnelStream) TunnelStream {
	return &measuredControlStream{TunnelStream: stream, manager: m, session: sess}
}

func (m *TunnelManager) ConnectionMetrics() ConnectionMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.metrics
	if s.Dial != nil {
		copy := *s.Dial
		s.Dial = &copy
	}
	return s
}

// RecordControlReady measures from transport establishment to the authenticated
// control reply, including control-stream setup and approval exchange.
func (m *TunnelManager) RecordControlReady(sess TunnelSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == sess && !m.connectedAt.IsZero() {
		m.metrics.ControlReadyMS = time.Since(m.connectedAt).Milliseconds()
	}
}
