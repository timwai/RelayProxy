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
	CongestionController string  `json:"congestion_controller,omitempty"`
	CongestionTargetBPS  uint64  `json:"congestion_target_bps,omitempty"`
	MinRTTMS             float64 `json:"min_rtt_ms"`
	LatestRTTMS          float64 `json:"latest_rtt_ms"`
	SmoothedRTTMS        float64 `json:"smoothed_rtt_ms"`
	RTTDeviationMS       float64 `json:"rtt_deviation_ms"`
	BytesSent            uint64  `json:"bytes_sent"`
	BytesReceived        uint64  `json:"bytes_received"`
	SendBPS              uint64  `json:"send_bps"`
	ReceiveBPS           uint64  `json:"receive_bps"`
	PacketsSent          uint64  `json:"packets_sent"`
	PacketsReceived      uint64  `json:"packets_received"`
	SentBytesLost        uint64  `json:"sent_bytes_lost"`
	SentPacketsLost      uint64  `json:"sent_packets_lost"`
	SentByteLossPct      float64 `json:"sent_byte_loss_pct"`
	SentPacketLossPct    float64 `json:"sent_packet_loss_pct"`
	GSO                  bool    `json:"gso"`
	UDPReadBufferBytes   int     `json:"udp_read_buffer_bytes,omitempty"`
	UDPWriteBufferBytes  int     `json:"udp_write_buffer_bytes,omitempty"`
}

func lossPercent(lost, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(lost) * 100 / float64(total)
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
	if quicSession, ok := session.(*QUICSession); ok && quicSession.conn != nil {
		stats := quicSession.conn.ConnectionStats()
		sendBPS, receiveBPS := quicSession.sampleByteRates(time.Now(), stats.BytesSent, stats.BytesReceived)
		state := quicSession.conn.ConnectionState()
		quicSession.diagnosticsMu.Lock()
		controller := quicSession.congestionController
		targetBPS := quicSession.congestionTargetBPS
		quicSession.diagnosticsMu.Unlock()
		d.QUIC = &QUICDiagnostics{
			CongestionController: controller,
			CongestionTargetBPS:  targetBPS,
			MinRTTMS:             float64(stats.MinRTT) / float64(time.Millisecond),
			LatestRTTMS:          float64(stats.LatestRTT) / float64(time.Millisecond),
			SmoothedRTTMS:        float64(stats.SmoothedRTT) / float64(time.Millisecond),
			RTTDeviationMS:       float64(stats.MeanDeviation) / float64(time.Millisecond),
			BytesSent:            stats.BytesSent,
			BytesReceived:        stats.BytesReceived,
			SendBPS:              sendBPS,
			ReceiveBPS:           receiveBPS,
			PacketsSent:          stats.PacketsSent,
			PacketsReceived:      stats.PacketsReceived,
			SentBytesLost:        stats.BytesLost,
			SentPacketsLost:      stats.PacketsLost,
			SentByteLossPct:      lossPercent(stats.BytesLost, stats.BytesSent),
			SentPacketLossPct:    lossPercent(stats.PacketsLost, stats.PacketsSent),
			GSO:                  state.GSO,
			UDPReadBufferBytes:   quicSession.udpReadBufferBytes,
			UDPWriteBufferBytes:  quicSession.udpWriteBufferBytes,
		}
	}
	return d
}
