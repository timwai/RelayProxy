package congestion

import (
	"github.com/apernet/quic-go"

	"relayproxy/internal/congestion/brutal"
)

const bitsPerMegabit = 1_000_000

// UseBrutal installs Hysteria's fixed-rate Brutal congestion controller.
// txBPS is bytes per second, matching quic-go's congestion.ByteCount units.
func UseBrutal(conn *quic.Conn, txBPS uint64, disableLossCompensation bool) {
	if conn == nil || txBPS == 0 {
		return
	}
	conn.SetCongestionControl(brutal.NewBrutalSender(txBPS, disableLossCompensation))
}

// MbpsToBytesPerSecond converts a decimal Mbps configuration value to bytes/s.
func MbpsToBytesPerSecond(mbps int) uint64 {
	if mbps <= 0 {
		return 0
	}
	return uint64(mbps) * bitsPerMegabit / 8
}

// CapRequestedRate applies a non-zero server cap to a client-requested rate.
// A zero request means "do not use Brutal"; a zero cap means unlimited.
func CapRequestedRate(requested, cap uint64) uint64 {
	if requested == 0 {
		return 0
	}
	if cap > 0 && requested > cap {
		return cap
	}
	return requested
}
