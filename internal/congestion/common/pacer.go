package common

import (
	"time"

	"github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/monotime"
)

const (
	maxBurstPackets               = 10
	maxBurstPacingDelayMultiplier = 4
)

// Pacer implements a token bucket pacing algorithm.
type Pacer struct {
	budgetAtLastSent congestion.ByteCount
	maxDatagramSize  congestion.ByteCount
	lastSentTime     monotime.Time
	getBandwidth     func() congestion.ByteCount // in bytes/s
}

func NewPacer(getBandwidth func() congestion.ByteCount) *Pacer {
	p := &Pacer{
		budgetAtLastSent: maxBurstPackets * congestion.InitialPacketSize,
		maxDatagramSize:  congestion.InitialPacketSize,
		getBandwidth:     getBandwidth,
	}
	return p
}

func (p *Pacer) SentPacket(sendTime monotime.Time, size congestion.ByteCount) {
	budget := p.Budget(sendTime)
	if size > budget {
		p.budgetAtLastSent = 0
	} else {
		p.budgetAtLastSent = budget - size
	}
	p.lastSentTime = sendTime
}

func (p *Pacer) Budget(now monotime.Time) congestion.ByteCount {
	if p.lastSentTime.IsZero() {
		return p.maxBurstSize()
	}
	maxBudget := p.maxBurstSize()
	if p.budgetAtLastSent >= maxBudget {
		return maxBudget
	}
	elapsed := now.Sub(p.lastSentTime)
	bandwidth := p.getBandwidth()
	if elapsed <= 0 || bandwidth <= 0 {
		return max(congestion.ByteCount(0), p.budgetAtLastSent)
	}
	// Avoid multiplying a potentially high bytes/s value by elapsed
	// nanoseconds before checking the burst cap. At 1,000,000 Mbps,
	// even a 100 ms scheduling pause overflows int64 in the old code.
	remaining := maxBudget - p.budgetAtLastSent
	if float64(bandwidth)*elapsed.Seconds() >= float64(remaining) {
		return maxBudget
	}
	return min(maxBudget, p.budgetAtLastSent+
		congestion.ByteCount(float64(bandwidth)*elapsed.Seconds()))
}

func (p *Pacer) maxBurstSize() congestion.ByteCount {
	return max(
		congestion.ByteCount((maxBurstPacingDelayMultiplier*congestion.MinPacingDelay).Nanoseconds())*p.getBandwidth()/1e9,
		maxBurstPackets*p.maxDatagramSize,
	)
}

// TimeUntilSend returns when the next packet should be sent.
// It returns the zero value if a packet can be sent immediately.
func (p *Pacer) TimeUntilSend() monotime.Time {
	if p.budgetAtLastSent >= p.maxDatagramSize {
		return 0
	}
	diff := 1e9 * uint64(p.maxDatagramSize-p.budgetAtLastSent)
	bw := uint64(p.getBandwidth())
	// We might need to round up this value.
	// Otherwise, we might have a budget (slightly) smaller than the datagram size when the timer expires.
	d := diff / bw
	// this is effectively a math.Ceil, but using only integer math
	if diff%bw > 0 {
		d++
	}
	return p.lastSentTime.Add(max(congestion.MinPacingDelay, time.Duration(d)*time.Nanosecond))
}

func (p *Pacer) SetMaxDatagramSize(s congestion.ByteCount) {
	p.maxDatagramSize = s
}
