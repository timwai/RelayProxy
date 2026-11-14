package desktop

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ImpairmentConfig describes a deterministic, cross-platform network
// impairment profile for a DatagramPath. It is intended for tests, diagnostics
// and repeatable tuning of Relay Desktop path/ABR policy without depending on
// Linux tc/netem.
type ImpairmentConfig struct {
	Latency time.Duration
	Jitter  time.Duration

	// LossPercent applies deterministic pseudo-random loss in [0, 100].
	LossPercent float64

	// BurstEvery/BurstLength drop the first BurstLength packets in every
	// BurstEvery-packet cycle. A zero BurstEvery disables burst loss.
	BurstEvery  uint64
	BurstLength uint64

	// BandwidthBytesPerSecond serializes packets through a simple deterministic
	// rate limiter. Zero means unlimited.
	BandwidthBytesPerSecond int64

	// Seed makes random loss/jitter deterministic across runs.
	Seed uint64
}

func (c ImpairmentConfig) normalized() ImpairmentConfig {
	if c.Latency < 0 {
		c.Latency = 0
	}
	if c.Jitter < 0 {
		c.Jitter = 0
	}
	if c.LossPercent < 0 {
		c.LossPercent = 0
	}
	if c.LossPercent > 100 {
		c.LossPercent = 100
	}
	if c.BurstEvery == 0 {
		c.BurstLength = 0
	} else if c.BurstLength > c.BurstEvery {
		c.BurstLength = c.BurstEvery
	}
	if c.BandwidthBytesPerSecond < 0 {
		c.BandwidthBytesPerSecond = 0
	}
	return c
}

type ImpairmentStats struct {
	SendPackets         uint64
	SendDropped         uint64
	ReceivePackets      uint64
	ReceiveDropped      uint64
	SendPayloadBytes    uint64
	ReceivePayloadBytes uint64
}

// ImpairmentPath wraps an existing DatagramPath and applies an updateable,
// deterministic impairment profile in both directions.
type ImpairmentPath struct {
	base DatagramPath

	cfgMu sync.RWMutex
	cfg   ImpairmentConfig

	sendSeq atomic.Uint64
	recvSeq atomic.Uint64

	sendPackets atomic.Uint64
	sendDropped atomic.Uint64
	recvPackets atomic.Uint64
	recvDropped atomic.Uint64
	sendBytes   atomic.Uint64
	recvBytes   atomic.Uint64

	sendRateMu sync.Mutex
	sendNext   time.Time
	recvRateMu sync.Mutex
	recvNext   time.Time

	closeOnce sync.Once
	closeErr  error
}

func NewImpairmentPath(base DatagramPath, cfg ImpairmentConfig) *ImpairmentPath {
	return &ImpairmentPath{base: base, cfg: cfg.normalized()}
}

func (p *ImpairmentPath) Name() string {
	if p == nil || p.base == nil {
		return "impairment"
	}
	return fmt.Sprintf("impairment(%s)", p.base.Name())
}

// UpdateConfig changes the active impairment profile without replacing the
// wrapped path. Bandwidth reservations are reset so a recovery profile takes
// effect immediately rather than draining an obsolete synthetic queue.
func (p *ImpairmentPath) UpdateConfig(cfg ImpairmentConfig) {
	if p == nil {
		return
	}
	p.cfgMu.Lock()
	p.cfg = cfg.normalized()
	p.cfgMu.Unlock()

	p.sendRateMu.Lock()
	p.sendNext = time.Time{}
	p.sendRateMu.Unlock()
	p.recvRateMu.Lock()
	p.recvNext = time.Time{}
	p.recvRateMu.Unlock()
}

func (p *ImpairmentPath) Config() ImpairmentConfig {
	if p == nil {
		return ImpairmentConfig{}
	}
	p.cfgMu.RLock()
	defer p.cfgMu.RUnlock()
	return p.cfg
}

func (p *ImpairmentPath) Stats() ImpairmentStats {
	if p == nil {
		return ImpairmentStats{}
	}
	return ImpairmentStats{
		SendPackets:         p.sendPackets.Load(),
		SendDropped:         p.sendDropped.Load(),
		ReceivePackets:      p.recvPackets.Load(),
		ReceiveDropped:      p.recvDropped.Load(),
		SendPayloadBytes:    p.sendBytes.Load(),
		ReceivePayloadBytes: p.recvBytes.Load(),
	}
}

func (p *ImpairmentPath) Send(ctx context.Context, payload []byte) error {
	if p == nil || p.base == nil {
		return context.Canceled
	}
	seq := p.sendSeq.Add(1)
	p.sendPackets.Add(1)
	if len(payload) > 0 {
		p.sendBytes.Add(uint64(len(payload)))
	}
	cfg := p.Config()
	if impairmentDrop(cfg, seq, impairmentSendSalt) {
		p.sendDropped.Add(1)
		return nil
	}
	delay := impairmentDelay(cfg, seq, impairmentSendSalt)
	delay += p.reserveBandwidth(true, len(payload), cfg.BandwidthBytesPerSecond)
	if err := waitImpairment(ctx, delay); err != nil {
		return err
	}
	return p.base.Send(ctx, payload)
}

func (p *ImpairmentPath) Receive(ctx context.Context) ([]byte, error) {
	if p == nil || p.base == nil {
		return nil, context.Canceled
	}
	for {
		payload, err := p.base.Receive(ctx)
		if err != nil {
			return nil, err
		}
		seq := p.recvSeq.Add(1)
		p.recvPackets.Add(1)
		if len(payload) > 0 {
			p.recvBytes.Add(uint64(len(payload)))
		}
		cfg := p.Config()
		if impairmentDrop(cfg, seq, impairmentReceiveSalt) {
			p.recvDropped.Add(1)
			continue
		}
		delay := impairmentDelay(cfg, seq, impairmentReceiveSalt)
		delay += p.reserveBandwidth(false, len(payload), cfg.BandwidthBytesPerSecond)
		if err := waitImpairment(ctx, delay); err != nil {
			return nil, err
		}
		return payload, nil
	}
}

func (p *ImpairmentPath) Close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		if p.base != nil {
			p.closeErr = p.base.Close()
		}
	})
	return p.closeErr
}

func (p *ImpairmentPath) reserveBandwidth(send bool, bytes int, rate int64) time.Duration {
	if p == nil || bytes <= 0 || rate <= 0 {
		return 0
	}
	now := time.Now()
	if send {
		p.sendRateMu.Lock()
		delay := reserveBandwidthDelay(now, &p.sendNext, bytes, rate)
		p.sendRateMu.Unlock()
		return delay
	}
	p.recvRateMu.Lock()
	delay := reserveBandwidthDelay(now, &p.recvNext, bytes, rate)
	p.recvRateMu.Unlock()
	return delay
}

func reserveBandwidthDelay(now time.Time, next *time.Time, bytes int, rate int64) time.Duration {
	if bytes <= 0 || rate <= 0 || next == nil {
		return 0
	}
	service := time.Duration((int64(bytes)*int64(time.Second) + rate - 1) / rate)
	start := now
	if next.After(start) {
		start = *next
	}
	finish := start.Add(service)
	*next = finish
	return finish.Sub(now)
}

func waitImpairment(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const (
	impairmentSendSalt    uint64 = 0x9e3779b97f4a7c15
	impairmentReceiveSalt uint64 = 0xd1b54a32d192ed03
)

func impairmentDrop(cfg ImpairmentConfig, seq, salt uint64) bool {
	cfg = cfg.normalized()
	if cfg.BurstEvery > 0 && cfg.BurstLength > 0 {
		position := (seq - 1) % cfg.BurstEvery
		if position < cfg.BurstLength {
			return true
		}
	}
	if cfg.LossPercent <= 0 {
		return false
	}
	if cfg.LossPercent >= 100 {
		return true
	}
	return impairmentUnit(cfg.Seed, seq, salt)*100 < cfg.LossPercent
}

func impairmentDelay(cfg ImpairmentConfig, seq, salt uint64) time.Duration {
	cfg = cfg.normalized()
	if cfg.Jitter == 0 {
		return cfg.Latency
	}
	unit := impairmentUnit(cfg.Seed, seq, salt)
	offset := time.Duration((unit*2 - 1) * float64(cfg.Jitter))
	delay := cfg.Latency + offset
	if delay < 0 {
		return 0
	}
	return delay
}

func impairmentUnit(seed, seq, salt uint64) float64 {
	x := seed ^ salt ^ (seq * 0x9e3779b97f4a7c15)
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	x ^= x >> 31
	return float64(x>>11) / float64(uint64(1)<<53)
}
