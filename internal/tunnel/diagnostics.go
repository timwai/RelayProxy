package tunnel

import "time"

// SessionDiagnostics describes a tunnel from the endpoint collecting it. QUIC counters
// cover the whole session, and lost-sent counters say nothing about loss in
// the opposite direction. They can decrease after spurious loss is detected.
// When collected on the relay, sent counters describe relay-to-device traffic.
type SessionDiagnostics struct {
	Transport TransportType    `json:"transport"`
	Local     string           `json:"local"`
	Remote    string           `json:"remote"`
	QUIC      *QUICDiagnostics `json:"quic,omitempty"`
}

type QUICDiagnostics struct {
	MinRTTMS        float64 `json:"min_rtt_ms"`
	LatestRTTMS     float64 `json:"latest_rtt_ms"`
	SmoothedRTTMS   float64 `json:"smoothed_rtt_ms"`
	BytesSent       uint64  `json:"bytes_sent"`
	BytesReceived   uint64  `json:"bytes_received"`
	PacketsSent     uint64  `json:"packets_sent"`
	PacketsReceived uint64  `json:"packets_received"`
	SentBytesLost   uint64  `json:"sent_bytes_lost"`
	SentPacketsLost uint64  `json:"sent_packets_lost"`
}

func DiagnoseSession(session TunnelSession) *SessionDiagnostics {
	if session == nil {
		return nil
	}
	d := &SessionDiagnostics{Transport: session.Transport()}
	if addr := session.LocalAddr(); addr != nil {
		d.Local = addr.String()
	}
	if addr := session.RemoteAddr(); addr != nil {
		d.Remote = addr.String()
	}
	if quic, ok := session.(*QUICSession); ok && quic.conn != nil {
		s := quic.conn.ConnectionStats()
		d.QUIC = &QUICDiagnostics{
			MinRTTMS:      float64(s.MinRTT) / float64(time.Millisecond),
			LatestRTTMS:   float64(s.LatestRTT) / float64(time.Millisecond),
			SmoothedRTTMS: float64(s.SmoothedRTT) / float64(time.Millisecond),
			BytesSent:     s.BytesSent, BytesReceived: s.BytesReceived,
			PacketsSent: s.PacketsSent, PacketsReceived: s.PacketsReceived,
			SentBytesLost: s.BytesLost, SentPacketsLost: s.PacketsLost,
		}
	}
	return d
}
