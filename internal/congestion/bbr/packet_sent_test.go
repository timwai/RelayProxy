package bbr

import (
	"testing"
	"time"

	"github.com/apernet/quic-go/congestion"
	"github.com/apernet/quic-go/monotime"
)

type packetTestClock struct{ now monotime.Time }

func (c packetTestClock) Now() monotime.Time { return c.now }

type packetTestRTT struct{ congestion.RTTStatsProvider }

func (packetTestRTT) MinRTT() time.Duration { return 50 * time.Millisecond }

func TestPostSendInflightInitializesDeliverySample(t *testing.T) {
	for _, profile := range []Profile{ProfileStandard, ProfileAggressive} {
		now := monotime.Now()
		b := NewBbrSender(packetTestClock{now}, 1200, profile)
		b.SetRTTStatsProvider(packetTestRTT{})
		b.OnPacketSent(now, 1200, 1, 1200, true)
		state := b.sampler.connectionStateMap.GetEntry(1)
		if state == nil || state.sendTimeState.bytesInFlight != 1200 {
			t.Fatalf("packet counted twice: %+v", state)
		}
		b.OnCongestionEventEx(1200, now.Add(50*time.Millisecond), []congestion.AckedPacketInfo{{PacketNumber: 1, BytesAcked: 1200}}, nil)
		if b.bandwidthEstimate() == 0 {
			t.Fatal("first packet produced no delivery sample")
		}
	}
}

func TestIdleResumeDoesNotForceProbeRTT(t *testing.T) {
	now := monotime.Now()
	b := NewBbrSender(packetTestClock{now}, 1200, ProfileAggressive)
	b.SetRTTStatsProvider(packetTestRTT{})
	b.mode, b.isAtFullBandwidth = bbrModeProbeBw, true
	b.congestionWindow = 1_000_000
	b.minRtt, b.minRttTimestamp = 50*time.Millisecond, now.Add(-11*time.Second)
	b.maxBandwidth.Reset(Bandwidth(100_000_000), 0)
	b.sampler.lastAckedPacketSentTime = now.Add(-11 * time.Second)
	b.sampler.lastAckedPacketAckTime = now.Add(-11 * time.Second)
	b.sampler.totalBytesSent, b.sampler.totalBytesAcked = 1200, 1200
	b.sampler.totalBytesSentAtLastAckedPacket = 1200
	b.OnPacketSent(now, 1200, 2, 1200, true)
	b.OnCongestionEventEx(1200, now.Add(50*time.Millisecond), []congestion.AckedPacketInfo{{PacketNumber: 2, BytesAcked: 1200}}, nil)
	if b.mode == bbrModeProbeRtt {
		t.Fatalf("idle resume collapsed window to %d", b.GetCongestionWindow())
	}
}

func TestACKOnlyPacketDoesNotSignalIdleResume(t *testing.T) {
	now := monotime.Now()
	b := NewBbrSender(packetTestClock{now}, 1200, ProfileAggressive)
	b.SetRTTStatsProvider(packetTestRTT{})
	b.OnPacketSent(now, 0, 1, 40, false)
	if b.exitingQuiescence || b.sampler.totalBytesSent != 0 {
		t.Fatal("ACK-only packet treated as data")
	}
}
