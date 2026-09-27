package testnetem

import (
	"net"
	"testing"
	"time"
)

type fakePacketConn struct {
	writes int
}

func (f *fakePacketConn) ReadFrom([]byte) (int, net.Addr, error) { return 0, nil, net.ErrClosed }
func (f *fakePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	f.writes++
	return len(p), nil
}
func (f *fakePacketConn) Close() error                     { return nil }
func (f *fakePacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (f *fakePacketConn) SetDeadline(time.Time) error      { return nil }
func (f *fakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakePacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestNewPacketConnRejectsNil(t *testing.T) {
	if _, err := NewPacketConn(nil, Profile{}); err == nil {
		t.Fatal("expected nil PacketConn to fail")
	}
}

func TestProfileNormalization(t *testing.T) {
	conn, err := NewPacketConn(&fakePacketConn{}, Profile{
		Delay:              -time.Second,
		Jitter:             -5 * time.Millisecond,
		LossPercent:        150,
		RateBytesPerSecond: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := conn.Profile()
	if got.Delay != 0 || got.Jitter != 5*time.Millisecond || got.LossPercent != 100 || got.RateBytesPerSecond != 0 {
		t.Fatalf("normalized profile=%+v", got)
	}
}

func TestWriteToHundredPercentLossDropsWithoutUnderlyingWrite(t *testing.T) {
	base := &fakePacketConn{}
	conn, err := NewPacketConn(base, Profile{LossPercent: 100})
	if err != nil {
		t.Fatal(err)
	}
	n, err := conn.WriteTo([]byte("desktop"), &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	if n != len("desktop") {
		t.Fatalf("reported write=%d", n)
	}
	if base.writes != 0 {
		t.Fatalf("underlying writes=%d want=0", base.writes)
	}
}

func TestBurstLossIsDeterministic(t *testing.T) {
	base := &fakePacketConn{}
	conn, err := NewPacketConn(base, Profile{
		BurstEveryPackets:  5,
		BurstLengthPackets: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := conn.WriteTo([]byte{byte(i)}, &net.UDPAddr{}); err != nil {
			t.Fatal(err)
		}
	}
	if base.writes != 6 {
		t.Fatalf("underlying writes=%d want=6", base.writes)
	}
}

func TestRateLimitBuildsWriteQueue(t *testing.T) {
	conn, err := NewPacketConn(&fakePacketConn{}, Profile{RateBytesPerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10, 0)
	conn.mu.Lock()
	first := conn.writeQueueDelayLocked(1000, now)
	second := conn.writeQueueDelayLocked(1000, now)
	conn.mu.Unlock()
	if first != 0 {
		t.Fatalf("first queue delay=%s want=0", first)
	}
	if second != time.Second {
		t.Fatalf("second queue delay=%s want=1s", second)
	}
}

func TestJitterSequenceIsReproducible(t *testing.T) {
	makeSeq := func() []time.Duration {
		conn, err := NewPacketConn(&fakePacketConn{}, Profile{
			Delay:  20 * time.Millisecond,
			Jitter: 5 * time.Millisecond,
			Seed:   42,
		})
		if err != nil {
			t.Fatal(err)
		}
		conn.mu.Lock()
		defer conn.mu.Unlock()
		return []time.Duration{
			conn.packetDelayLocked(),
			conn.packetDelayLocked(),
			conn.packetDelayLocked(),
		}
	}
	a, b := makeSeq(), makeSeq()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("delay[%d]=%s want=%s", i, a[i], b[i])
		}
		if a[i] < 15*time.Millisecond || a[i] > 25*time.Millisecond {
			t.Fatalf("delay[%d]=%s outside configured jitter window", i, a[i])
		}
	}
}

func TestSetProfileStartsNewDeterministicPhase(t *testing.T) {
	conn, err := NewPacketConn(&fakePacketConn{}, Profile{
		Delay:  20 * time.Millisecond,
		Jitter: 5 * time.Millisecond,
		Seed:   42,
	})
	if err != nil {
		t.Fatal(err)
	}

	conn.mu.Lock()
	first := conn.packetDelayLocked()
	_ = conn.writeQueueDelayLocked(1000, time.Unix(10, 0))
	conn.mu.Unlock()

	conn.SetProfile(Profile{
		Delay:              40 * time.Millisecond,
		Jitter:             10 * time.Millisecond,
		RateBytesPerSecond: 2000,
		Seed:               99,
	})
	got := conn.Profile()
	if got.Delay != 40*time.Millisecond || got.Jitter != 10*time.Millisecond || got.RateBytesPerSecond != 2000 || got.Seed != 99 {
		t.Fatalf("profile after change=%+v", got)
	}

	conn.mu.Lock()
	changedFirst := conn.packetDelayLocked()
	queueDelay := conn.writeQueueDelayLocked(1000, time.Unix(20, 0))
	conn.mu.Unlock()
	if changedFirst < 30*time.Millisecond || changedFirst > 50*time.Millisecond {
		t.Fatalf("changed first delay=%s outside configured window", changedFirst)
	}
	if queueDelay != 0 {
		t.Fatalf("new impairment phase retained old shaper backlog: %s", queueDelay)
	}

	conn.SetProfile(Profile{
		Delay:  20 * time.Millisecond,
		Jitter: 5 * time.Millisecond,
		Seed:   42,
	})
	conn.mu.Lock()
	replayedFirst := conn.packetDelayLocked()
	conn.mu.Unlock()
	if replayedFirst != first {
		t.Fatalf("replayed deterministic delay=%s want=%s", replayedFirst, first)
	}
}

func TestCombinedImpairmentProfileIsDeterministic(t *testing.T) {
	type sample struct {
		drop  bool
		delay time.Duration
		queue time.Duration
	}

	run := func() []sample {
		conn, err := NewPacketConn(&fakePacketConn{}, Profile{
			Delay:              30 * time.Millisecond,
			Jitter:             12 * time.Millisecond,
			LossPercent:        18,
			BurstEveryPackets:  7,
			BurstLengthPackets: 2,
			RateBytesPerSecond: 12_000,
			Seed:               20260922,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(40, 0)
		out := make([]sample, 18)
		conn.mu.Lock()
		defer conn.mu.Unlock()
		for i := range out {
			out[i] = sample{
				drop:  conn.shouldDropLocked(),
				delay: conn.packetDelayLocked(),
				queue: conn.writeQueueDelayLocked(1200, now),
			}
		}
		return out
	}

	first, second := run(), run()
	drops := 0
	queueBuilt := false
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("combined impairment sample %d is not reproducible: %+v != %+v", i, first[i], second[i])
		}
		if first[i].drop {
			drops++
		}
		if first[i].delay < 18*time.Millisecond || first[i].delay > 42*time.Millisecond {
			t.Fatalf("sample %d delay=%s outside jitter window", i, first[i].delay)
		}
		if first[i].queue > 0 {
			queueBuilt = true
		}
	}
	if drops < 6 {
		t.Fatalf("combined profile drops=%d, expected burst plus random loss", drops)
	}
	if !queueBuilt {
		t.Fatal("combined profile did not build a bandwidth-shaping queue")
	}
}
