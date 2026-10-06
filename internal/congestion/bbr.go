package congestion

import (
	"github.com/apernet/quic-go"
	quiccongestion "github.com/apernet/quic-go/congestion"

	"relayproxy/internal/congestion/bbr"
)

// UseBBR replaces the QUIC connection's default loss-based congestion
// controller with Hysteria's BBR implementation.
//
// The replacement controller must be seeded at or below the packet size the
// QUIC connection actually started with. This keeps path-MTU discovery
// monotonic from the controller's perspective.
func UseBBR(conn *quic.Conn, profile bbr.Profile) {
	if conn == nil {
		return
	}
	initialPacketSize := seedPacketSize(
		conn.InitialPacketSize(),
		bbr.GetInitialPacketSize(conn.RemoteAddr()),
	)
	conn.SetCongestionControl(bbr.NewBbrSender(
		bbr.DefaultClock{},
		initialPacketSize,
		profile,
	))
}

func UseDefaultBBR(conn *quic.Conn) {
	UseBBR(conn, bbr.ProfileStandard)
}

func seedPacketSize(quicSize, byAddr quiccongestion.ByteCount) quiccongestion.ByteCount {
	if quicSize <= 0 {
		return byAddr
	}
	return min(quicSize, byAddr)
}
