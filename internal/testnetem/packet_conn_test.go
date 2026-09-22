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
func (f *fakePacketConn) Close() error                       { return nil }
func (f *fakePacketConn) LocalAddr() net.Addr                { return &net.UDPAddr{} }
func (f *fakePacketConn) SetDeadline(time.Time) error        { return nil }
func (f *fakePacketConn) SetReadDeadline(time.Time) error    { return nil }
func (f *fakePacketConn) SetWriteDeadline(time.Time) error   { return nil }

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
