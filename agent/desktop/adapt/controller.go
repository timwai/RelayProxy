package adapt

import (
	"math"
	"time"

	"relayproxy/internal/protocol"
)

type NetworkEstimate struct {
	DeliveryRate int64
	RTT          time.Duration
	Jitter       time.Duration
	Loss         float64
	QueueDelay   time.Duration
	Dropped      uint64
}

type MediaDecision struct {
	TargetBitrate int
	Changed       bool
	ForceIDR      bool
	Reason        string
}

type Config struct {
	Scene          protocol.DesktopScene
	MinBitrate     int
	MaxBitrate     int
	InitialBitrate int
	StableWindows  int
	IncreaseRatio  float64
	IncreaseFloor  int
}

func DefaultConfig(scene protocol.DesktopScene, maxBitrate int) Config {
	if maxBitrate <= 0 {
		maxBitrate = 6_000_000
	}
	minBitrate := 500_000
	switch scene {
	case protocol.DesktopSceneGaming, protocol.DesktopScenePerformance:
		minBitrate = 1_000_000
	case protocol.DesktopSceneQuality:
		minBitrate = 1_500_000
	}
	if minBitrate > maxBitrate {
		minBitrate = maxBitrate
	}
	return Config{
		Scene:          scene,
		MinBitrate:     minBitrate,
		MaxBitrate:     maxBitrate,
		InitialBitrate: maxBitrate,
		StableWindows:  4,
		IncreaseRatio:  1.08,
		IncreaseFloor:  150_000,
	}
}

type Controller struct {
	cfg Config

	target      int
	stable      int
	lastDropped uint64
}

func NewController(cfg Config) *Controller {
	if cfg.MaxBitrate <= 0 {
		cfg.MaxBitrate = 6_000_000
	}
	if cfg.MinBitrate <= 0 {
		cfg.MinBitrate = 500_000
	}
	if cfg.MinBitrate > cfg.MaxBitrate {
		cfg.MinBitrate = cfg.MaxBitrate
	}
	if cfg.InitialBitrate <= 0 {
		cfg.InitialBitrate = cfg.MaxBitrate
	}
	if cfg.InitialBitrate < cfg.MinBitrate {
		cfg.InitialBitrate = cfg.MinBitrate
	}
	if cfg.InitialBitrate > cfg.MaxBitrate {
		cfg.InitialBitrate = cfg.MaxBitrate
	}
	if cfg.StableWindows <= 0 {
		cfg.StableWindows = 4
	}
	if cfg.IncreaseRatio <= 1 {
		cfg.IncreaseRatio = 1.08
	}
	if cfg.IncreaseFloor <= 0 {
		cfg.IncreaseFloor = 150_000
	}
	return &Controller{cfg: cfg, target: cfg.InitialBitrate}
}

func (c *Controller) TargetBitrate() int {
	if c == nil {
		return 0
	}
	return c.target
}

func (c *Controller) Observe(stats protocol.DesktopSessionStats) MediaDecision {
	if c == nil {
		return MediaDecision{}
	}
	droppedDelta := uint64(0)
	if stats.DroppedFrames >= c.lastDropped {
		droppedDelta = stats.DroppedFrames - c.lastDropped
	}
	c.lastDropped = stats.DroppedFrames

	estimate := NetworkEstimate{
		DeliveryRate: stats.DeliveryRate,
		RTT:          time.Duration(stats.RTTMs * float64(time.Millisecond)),
		Jitter:       time.Duration(stats.JitterMs * float64(time.Millisecond)),
		Loss:         stats.LossPercent,
		QueueDelay:   time.Duration(stats.SendQueueDelayMs * float64(time.Millisecond)),
		Dropped:      droppedDelta,
	}
	return c.observeEstimate(estimate)
}

func (c *Controller) observeEstimate(estimate NetworkEstimate) MediaDecision {
	factor, reason := c.degradeFactor(estimate)
	if factor < 1 {
		c.stable = 0
		next := int(math.Floor(float64(c.target) * factor))
		if next < c.cfg.MinBitrate {
			next = c.cfg.MinBitrate
		}
		if next >= c.target {
			return MediaDecision{TargetBitrate: c.target}
		}
		c.target = next
		return MediaDecision{
			TargetBitrate: c.target,
			Changed:       true,
			Reason:        reason,
		}
	}

	if !c.stableEstimate(estimate) {
		c.stable = 0
		return MediaDecision{TargetBitrate: c.target}
	}
	if c.target >= c.cfg.MaxBitrate {
		c.stable = 0
		return MediaDecision{TargetBitrate: c.target}
	}
	c.stable++
	if c.stable < c.cfg.StableWindows {
		return MediaDecision{TargetBitrate: c.target}
	}
	c.stable = 0

	ratioIncrease := int(math.Ceil(float64(c.target) * (c.cfg.IncreaseRatio - 1)))
	if ratioIncrease < c.cfg.IncreaseFloor {
		ratioIncrease = c.cfg.IncreaseFloor
	}
	next := c.target + ratioIncrease
	if next > c.cfg.MaxBitrate {
		next = c.cfg.MaxBitrate
	}
	if next == c.target {
		return MediaDecision{TargetBitrate: c.target}
	}
	c.target = next
	return MediaDecision{
		TargetBitrate: c.target,
		Changed:       true,
		Reason:        "stable_recovery",
	}
}

func (c *Controller) degradeFactor(estimate NetworkEstimate) (float64, string) {
	switch {
	case estimate.Dropped >= 3:
		return 0.60, "frame_loss"
	case estimate.Loss >= 5:
		return 0.60, "severe_loss"
	case estimate.QueueDelay >= 120*time.Millisecond:
		return 0.60, "severe_queue"
	case estimate.Jitter >= 80*time.Millisecond:
		return 0.65, "severe_jitter"
	case estimate.RTT >= 350*time.Millisecond:
		return 0.70, "severe_rtt"
	case estimate.Dropped > 0:
		return 0.78, "frame_loss"
	case estimate.Loss >= 2:
		return 0.75, "loss"
	case estimate.QueueDelay >= 60*time.Millisecond:
		return 0.75, "queue"
	case estimate.Jitter >= 40*time.Millisecond:
		return 0.80, "jitter"
	case estimate.RTT >= 250*time.Millisecond:
		return 0.85, "high_rtt"
	case estimate.Loss >= 1:
		return 0.90, "mild_loss"
	case estimate.QueueDelay >= 30*time.Millisecond:
		return 0.90, "mild_queue"
	case estimate.Jitter >= 25*time.Millisecond:
		return 0.90, "mild_jitter"
	default:
		return 1, ""
	}
}

func (c *Controller) stableEstimate(estimate NetworkEstimate) bool {
	if estimate.Dropped != 0 ||
		estimate.Loss > 0.3 ||
		estimate.Jitter > 15*time.Millisecond ||
		estimate.QueueDelay > 15*time.Millisecond {
		return false
	}
	// A long but stable path can still carry a high bitrate. RTT alone only
	// blocks recovery once it is large enough to make interactive use suspect.
	return estimate.RTT <= 220*time.Millisecond || estimate.RTT == 0
}
