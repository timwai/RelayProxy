package desktop

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

type impairmentTestPath struct {
	mu      sync.Mutex
	sent    [][]byte
	receive [][]byte
	closed  int
}

func (p *impairmentTestPath) Name() string { return "udp_p2p" }

func (p *impairmentTestPath) Send(_ context.Context, payload []byte) error {
	p.mu.Lock()
	p.sent = append(p.sent, append([]byte(nil), payload...))
	p.mu.Unlock()
	return nil
}

func (p *impairmentTestPath) Receive(context.Context) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.receive) == 0 {
		return nil, io.EOF
	}
	payload := append([]byte(nil), p.receive[0]...)
	p.receive = p.receive[1:]
	return payload, nil
}

func (p *impairmentTestPath) Close() error {
	p.mu.Lock()
	p.closed++
	p.mu.Unlock()
	return nil
}

func (p *impairmentTestPath) sentCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

func TestImpairmentPathDeterministicBurstLossAndRecovery(t *testing.T) {
	base := &impairmentTestPath{}
	path := NewImpairmentPath(base, ImpairmentConfig{
		BurstEvery:  4,
		BurstLength: 1,
		Seed:        7,
	})
	for i := 0; i < 8; i++ {
		if err := path.Send(context.Background(), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := base.sentCount(); got != 6 {
		t.Fatalf("underlying sends=%d want=6", got)
	}
	stats := path.Stats()
	if stats.SendPackets != 8 || stats.SendDropped != 2 {
		t.Fatalf("stats=%+v", stats)
	}

	path.UpdateConfig(ImpairmentConfig{})
	if err := path.Send(context.Background(), []byte("recovered")); err != nil {
		t.Fatal(err)
	}
	if got := base.sentCount(); got != 7 {
		t.Fatalf("recovery send count=%d want=7", got)
	}
}

func TestImpairmentPathReceiveDropsAndContinues(t *testing.T) {
	base := &impairmentTestPath{receive: [][]byte{
		[]byte("drop-a"),
		[]byte("keep-b"),
		[]byte("drop-c"),
		[]byte("keep-d"),
	}}
	path := NewImpairmentPath(base, ImpairmentConfig{
		BurstEvery:  2,
		BurstLength: 1,
	})

	first, err := path.Receive(context.Background())
	if err != nil || string(first) != "keep-b" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	second, err := path.Receive(context.Background())
	if err != nil || string(second) != "keep-d" {
		t.Fatalf("second=%q err=%v", second, err)
	}
	stats := path.Stats()
	if stats.ReceivePackets != 4 || stats.ReceiveDropped != 2 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestImpairmentRandomLossAndJitterAreRepeatable(t *testing.T) {
	cfg := ImpairmentConfig{
		Latency:     40 * time.Millisecond,
		Jitter:      15 * time.Millisecond,
		LossPercent: 27,
		Seed:        12345,
	}
	cfgOther := cfg
	cfgOther.Seed = 54321

	different := false
	for seq := uint64(1); seq <= 64; seq++ {
		a := impairmentDrop(cfg, seq, impairmentSendSalt)
		b := impairmentDrop(cfg, seq, impairmentSendSalt)
		if a != b {
			t.Fatalf("same seed produced different loss at seq=%d", seq)
		}
		if a != impairmentDrop(cfgOther, seq, impairmentSendSalt) {
			different = true
		}

		delayA := impairmentDelay(cfg, seq, impairmentReceiveSalt)
		delayB := impairmentDelay(cfg, seq, impairmentReceiveSalt)
		if delayA != delayB {
			t.Fatalf("same seed produced different jitter at seq=%d", seq)
		}
		if delayA < 25*time.Millisecond || delayA > 55*time.Millisecond {
			t.Fatalf("delay=%s outside jitter bounds", delayA)
		}
	}
	if !different {
		t.Fatal("different seeds produced identical 64-packet loss pattern")
	}
}

func TestReserveBandwidthDelayModelsQueueAndRecovery(t *testing.T) {
	now := time.Unix(100, 0)
	var next time.Time

	first := reserveBandwidthDelay(now, &next, 1000, 1000)
	if first != time.Second {
		t.Fatalf("first delay=%s want=1s", first)
	}
	second := reserveBandwidthDelay(now, &next, 1000, 1000)
	if second != 2*time.Second {
		t.Fatalf("second delay=%s want=2s", second)
	}

	// After enough wall time has passed the old synthetic queue is drained.
	third := reserveBandwidthDelay(now.Add(3*time.Second), &next, 500, 1000)
	if third != 500*time.Millisecond {
		t.Fatalf("recovered delay=%s want=500ms", third)
	}
}

func TestImpairmentPathConfigNormalizationAndClose(t *testing.T) {
	base := &impairmentTestPath{}
	path := NewImpairmentPath(base, ImpairmentConfig{
		Latency:                 -time.Second,
		Jitter:                  -time.Second,
		LossPercent:             120,
		BurstEvery:              2,
		BurstLength:             8,
		BandwidthBytesPerSecond: -1,
	})
	cfg := path.Config()
	if cfg.Latency != 0 || cfg.Jitter != 0 || cfg.LossPercent != 100 ||
		cfg.BurstLength != 2 || cfg.BandwidthBytesPerSecond != 0 {
		t.Fatalf("normalized config=%+v", cfg)
	}
	if err := path.Close(); err != nil {
		t.Fatal(err)
	}
	if err := path.Close(); err != nil {
		t.Fatal(err)
	}
	base.mu.Lock()
	closed := base.closed
	base.mu.Unlock()
	if closed != 1 {
		t.Fatalf("base close count=%d want=1", closed)
	}
}
