package testnetem

import (
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"
)

// Profile describes deterministic packet impairments for Relay Desktop tests.
// A non-zero Seed makes jitter/loss sequences reproducible across runs.
type Profile struct {
	Delay              time.Duration
	Jitter             time.Duration
	LossPercent        float64
	BurstEveryPackets  uint64
	BurstLengthPackets uint64
	RateBytesPerSecond int64
	Seed               int64
}

func (p Profile) normalized() Profile {
	if p.Delay < 0 {
		p.Delay = 0
	}
	if p.Jitter < 0 {
		p.Jitter = -p.Jitter
	}
	if p.LossPercent < 0 {
		p.LossPercent = 0
	}
	if p.LossPercent > 100 {
		p.LossPercent = 100
	}
	if p.BurstEveryPackets == 0 {
		p.BurstLengthPackets = 0
	}
	if p.RateBytesPerSecond < 0 {
		p.RateBytesPerSecond = 0
	}
	if p.Seed == 0 {
		p.Seed = 1
	}
	return p
}

// PacketConn wraps a UDP-like PacketConn and applies deterministic delay,
// jitter, packet loss, burst loss and write-side bandwidth shaping.
type PacketConn struct {
	net.PacketConn

	mu        sync.Mutex
	profile   Profile
	rng       *rand.Rand
	packetSeq uint64
	nextWrite time.Time
}

func NewPacketConn(conn net.PacketConn, profile Profile) (*PacketConn, error) {
	if conn == nil {
		return nil, errors.New("nil packet conn")
	}
	profile = profile.normalized()
	return &PacketConn{
		PacketConn: conn,
		profile:    profile,
		rng:        rand.New(rand.NewSource(profile.Seed)),
	}, nil
}

func (c *PacketConn) Profile() Profile {
	if c == nil {
		return Profile{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profile
}

func (c *PacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c == nil || c.PacketConn == nil {
		return 0, net.ErrClosed
	}

	c.mu.Lock()
	drop := c.shouldDropLocked()
	delay := c.packetDelayLocked()
	queueDelay := c.writeQueueDelayLocked(len(p), time.Now())
	c.mu.Unlock()

	if drop {
		return len(p), nil
	}
	if err := sleepUntil(delay + queueDelay); err != nil {
		return 0, err
	}
	return c.PacketConn.WriteTo(p, addr)
}

func (c *PacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c == nil || c.PacketConn == nil {
		return 0, nil, net.ErrClosed
	}
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}

		c.mu.Lock()
		drop := c.shouldDropLocked()
		delay := c.packetDelayLocked()
		c.mu.Unlock()

		if drop {
			continue
		}
		if err := sleepUntil(delay); err != nil {
			return 0, nil, err
		}
		return n, addr, nil
	}
}

func (c *PacketConn) shouldDropLocked() bool {
	c.packetSeq++
	seq := c.packetSeq
	p := c.profile

	if p.BurstEveryPackets > 0 && p.BurstLengthPackets > 0 {
		offset := (seq - 1) % p.BurstEveryPackets
		if offset < p.BurstLengthPackets {
			return true
		}
	}
	if p.LossPercent <= 0 {
		return false
	}
	if p.LossPercent >= 100 {
		return true
	}
	return c.rng.Float64()*100 < p.LossPercent
}

func (c *PacketConn) packetDelayLocked() time.Duration {
	p := c.profile
	if p.Jitter <= 0 {
		return p.Delay
	}
	span := int64(p.Jitter)
	jitter := time.Duration(c.rng.Int63n(span*2+1) - span)
	delay := p.Delay + jitter
	if delay < 0 {
		return 0
	}
	return delay
}

func (c *PacketConn) writeQueueDelayLocked(size int, now time.Time) time.Duration {
	rate := c.profile.RateBytesPerSecond
	if rate <= 0 || size <= 0 {
		return 0
	}
	if c.nextWrite.Before(now) {
		c.nextWrite = now
	}
	queueDelay := c.nextWrite.Sub(now)
	serialize := time.Duration(float64(size) / float64(rate) * float64(time.Second))
	if serialize <= 0 {
		serialize = time.Nanosecond
	}
	c.nextWrite = c.nextWrite.Add(serialize)
	return queueDelay
}

func sleepUntil(d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	<-timer.C
	return nil
}
