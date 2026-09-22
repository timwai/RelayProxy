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
	TargetBitrate         int
	TargetFPS             int
	TargetResolutionScale int
	ResolutionChanged     bool
	Changed               bool
	ForceIDR              bool
	Reason                string
}

type Config struct {
	Scene                     protocol.DesktopScene
	MinBitrate                int
	MaxBitrate                int
	InitialBitrate            int
	MinFPS                    int
	MaxFPS                    int
	InitialFPS                int
	MinResolutionScale        int
	InitialResolutionScale    int
	StableWindows             int
	FPSPressureWindows        int
	FPSRecoveryWindows        int
	ResolutionPressureWindows int
	ResolutionRecoveryWindows int
	IncreaseRatio             float64
	IncreaseFloor             int
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
	minFPS := 10
	if scene == protocol.DesktopSceneGaming || scene == protocol.DesktopScenePerformance {
		minFPS = 30
	}
	minResolutionScale := 50
	if scene == protocol.DesktopSceneQuality {
		minResolutionScale = 75
	}
	return Config{
		Scene:                     scene,
		MinBitrate:                minBitrate,
		MaxBitrate:                maxBitrate,
		InitialBitrate:            maxBitrate,
		MinFPS:                    minFPS,
		MaxFPS:                    30,
		InitialFPS:                30,
		MinResolutionScale:        minResolutionScale,
		InitialResolutionScale:    100,
		StableWindows:             4,
		FPSPressureWindows:        4,
		FPSRecoveryWindows:        8,
		ResolutionPressureWindows: 8,
		ResolutionRecoveryWindows: 16,
		IncreaseRatio:             1.08,
		IncreaseFloor:             150_000,
	}
}

// AdaptiveMinResolutionScale returns the smallest percentage of the
// negotiated H.264 ceiling ABR may request. Quality mode preserves at least
// 75%; other scenes may reach 50%. Codec minimum dimensions can raise the
// floor for already-small sessions.
func AdaptiveMinResolutionScale(scene protocol.DesktopScene, maxWidth, maxHeight int) int {
	minimum := 50
	if scene == protocol.DesktopSceneQuality {
		minimum = 75
	}
	if maxWidth > 0 {
		widthFloor := (320*100 + maxWidth - 1) / maxWidth
		if widthFloor > minimum {
			minimum = widthFloor
		}
	}
	if maxHeight > 0 {
		heightFloor := (180*100 + maxHeight - 1) / maxHeight
		if heightFloor > minimum {
			minimum = heightFloor
		}
	}
	if minimum > 100 {
		return 100
	}
	return minimum
}

// AdaptiveMinFPS returns the lowest capture cadence ABR may choose for the
// negotiated scene. Gaming/performance preserve the negotiated FPS; desktop
// and quality-oriented scenes may trade motion cadence for realtime latency.
func AdaptiveMinFPS(scene protocol.DesktopScene, maxFPS int) int {
	if maxFPS <= 0 {
		return 0
	}
	if scene == protocol.DesktopSceneGaming || scene == protocol.DesktopScenePerformance {
		return maxFPS
	}
	minFPS := maxFPS / 3
	if minFPS < 5 {
		minFPS = 5
	}
	if minFPS > 15 {
		minFPS = 15
	}
	if minFPS > maxFPS {
		minFPS = maxFPS
	}
	return minFPS
}

type Controller struct {
	cfg Config

	target                int
	targetFPS             int
	targetResolutionScale int
	stable                int
	fpsPressure           int
	fpsStable             int
	resolutionPressure    int
	resolutionStable      int
	lastDropped           uint64
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
	if cfg.MaxFPS <= 0 {
		cfg.MaxFPS = 30
	}
	if cfg.InitialFPS <= 0 {
		cfg.InitialFPS = cfg.MaxFPS
	}
	if cfg.InitialFPS > cfg.MaxFPS {
		cfg.InitialFPS = cfg.MaxFPS
	}
	if cfg.MinFPS <= 0 {
		cfg.MinFPS = AdaptiveMinFPS(cfg.Scene, cfg.MaxFPS)
	}
	if cfg.MinFPS > cfg.MaxFPS {
		cfg.MinFPS = cfg.MaxFPS
	}
	if cfg.InitialFPS < cfg.MinFPS {
		cfg.InitialFPS = cfg.MinFPS
	}
	if cfg.StableWindows <= 0 {
		cfg.StableWindows = 4
	}
	if cfg.FPSPressureWindows <= 0 {
		cfg.FPSPressureWindows = 4
	}
	if cfg.FPSRecoveryWindows <= 0 {
		cfg.FPSRecoveryWindows = 8
	}
	if cfg.MinResolutionScale <= 0 {
		cfg.MinResolutionScale = 50
	}
	if cfg.MinResolutionScale < 50 {
		cfg.MinResolutionScale = 50
	}
	if cfg.MinResolutionScale > 100 {
		cfg.MinResolutionScale = 100
	}
	if cfg.InitialResolutionScale <= 0 {
		cfg.InitialResolutionScale = 100
	}
	if cfg.InitialResolutionScale > 100 {
		cfg.InitialResolutionScale = 100
	}
	if cfg.InitialResolutionScale < cfg.MinResolutionScale {
		cfg.InitialResolutionScale = cfg.MinResolutionScale
	}
	if cfg.ResolutionPressureWindows <= 0 {
		cfg.ResolutionPressureWindows = 8
	}
	if cfg.ResolutionRecoveryWindows <= 0 {
		cfg.ResolutionRecoveryWindows = 16
	}
	if cfg.IncreaseRatio <= 1 {
		cfg.IncreaseRatio = 1.08
	}
	if cfg.IncreaseFloor <= 0 {
		cfg.IncreaseFloor = 150_000
	}
	return &Controller{
		cfg:                   cfg,
		target:                cfg.InitialBitrate,
		targetFPS:             cfg.InitialFPS,
		targetResolutionScale: cfg.InitialResolutionScale,
	}
}

func (c *Controller) TargetBitrate() int {
	if c == nil {
		return 0
	}
	return c.target
}

func (c *Controller) TargetFPS() int {
	if c == nil {
		return 0
	}
	return c.targetFPS
}

func (c *Controller) TargetResolutionScale() int {
	if c == nil {
		return 0
	}
	return c.targetResolutionScale
}

func (c *Controller) SetResolutionScale(scale int) {
	if c == nil {
		return
	}
	if scale < c.cfg.MinResolutionScale {
		scale = c.cfg.MinResolutionScale
	}
	if scale > 100 {
		scale = 100
	}
	if scale <= 0 {
		scale = 100
	}
	c.targetResolutionScale = scale
	c.resolutionPressure = 0
	c.resolutionStable = 0
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

func nextLowerResolutionScale(current, minimum int) int {
	if current > 75 && minimum <= 75 {
		return 75
	}
	if current > minimum {
		return minimum
	}
	return current
}

func nextHigherResolutionScale(current int) int {
	if current < 75 {
		return 75
	}
	if current < 100 {
		return 100
	}
	return current
}

func (c *Controller) resolutionBitrateFloorReached() bool {
	if c == nil {
		return false
	}
	threshold := c.cfg.MaxBitrate * 45 / 100
	doubleFloor := c.cfg.MinBitrate * 2
	if doubleFloor > threshold {
		threshold = doubleFloor
	}
	if threshold > c.cfg.MaxBitrate {
		threshold = c.cfg.MaxBitrate
	}
	return c.target <= threshold
}

func (c *Controller) shouldReduceResolution(estimate NetworkEstimate) bool {
	if c == nil || c.targetResolutionScale <= c.cfg.MinResolutionScale {
		return false
	}
	return estimate.Dropped > 0 ||
		estimate.QueueDelay >= 120*time.Millisecond ||
		estimate.Loss >= 5 ||
		estimate.Jitter >= 80*time.Millisecond
}

func (c *Controller) observeEstimate(estimate NetworkEstimate) MediaDecision {
	factor, reason := c.degradeFactor(estimate)
	if factor < 1 {
		c.stable = 0
		c.fpsStable = 0
		c.resolutionStable = 0
		changed := false
		resolutionChanged := false

		next := int(math.Floor(float64(c.target) * factor))
		if next < c.cfg.MinBitrate {
			next = c.cfg.MinBitrate
		}
		if next < c.target {
			c.target = next
			changed = true
		}

		if c.shouldReduceFPS(estimate) && c.targetFPS > c.cfg.MinFPS {
			c.fpsPressure++
			if c.fpsPressure >= c.cfg.FPSPressureWindows {
				nextFPS := c.targetFPS * 3 / 4
				if nextFPS < c.cfg.MinFPS {
					nextFPS = c.cfg.MinFPS
				}
				if nextFPS < c.targetFPS {
					c.targetFPS = nextFPS
					changed = true
				}
				c.fpsPressure = 0
			}
		} else {
			c.fpsPressure = 0
		}

		if c.shouldReduceResolution(estimate) && c.resolutionBitrateFloorReached() {
			c.resolutionPressure++
			if c.resolutionPressure >= c.cfg.ResolutionPressureWindows {
				nextScale := nextLowerResolutionScale(c.targetResolutionScale, c.cfg.MinResolutionScale)
				if nextScale < c.targetResolutionScale {
					c.targetResolutionScale = nextScale
					changed = true
					resolutionChanged = true
					reason = "resolution_downshift"
				}
				c.resolutionPressure = 0
			}
		} else {
			c.resolutionPressure = 0
		}

		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
			ResolutionChanged:     resolutionChanged,
			Changed:               changed,
			Reason:                reason,
		}
	}

	c.fpsPressure = 0
	c.resolutionPressure = 0
	if !c.stableEstimate(estimate) {
		c.stable = 0
		c.fpsStable = 0
		c.resolutionStable = 0
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
		}
	}

	if c.target < c.cfg.MaxBitrate {
		c.fpsStable = 0
		c.resolutionStable = 0
		c.stable++
		if c.stable < c.cfg.StableWindows {
			return MediaDecision{
				TargetBitrate:         c.target,
				TargetFPS:             c.targetFPS,
				TargetResolutionScale: c.targetResolutionScale,
			}
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
			return MediaDecision{
				TargetBitrate:         c.target,
				TargetFPS:             c.targetFPS,
				TargetResolutionScale: c.targetResolutionScale,
			}
		}
		c.target = next
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
			Changed:               true,
			Reason:                "stable_recovery",
		}
	}

	c.stable = 0
	if c.targetFPS < c.cfg.MaxFPS {
		c.resolutionStable = 0
		c.fpsStable++
		if c.fpsStable < c.cfg.FPSRecoveryWindows {
			return MediaDecision{
				TargetBitrate:         c.target,
				TargetFPS:             c.targetFPS,
				TargetResolutionScale: c.targetResolutionScale,
			}
		}
		c.fpsStable = 0
		step := c.cfg.MaxFPS / 6
		if step < 1 {
			step = 1
		}
		nextFPS := c.targetFPS + step
		if nextFPS > c.cfg.MaxFPS {
			nextFPS = c.cfg.MaxFPS
		}
		c.targetFPS = nextFPS
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
			Changed:               true,
			Reason:                "fps_recovery",
		}
	}

	c.fpsStable = 0
	if c.targetResolutionScale >= 100 {
		c.resolutionStable = 0
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
		}
	}

	c.resolutionStable++
	if c.resolutionStable < c.cfg.ResolutionRecoveryWindows {
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
		}
	}
	c.resolutionStable = 0
	nextScale := nextHigherResolutionScale(c.targetResolutionScale)
	if nextScale == c.targetResolutionScale {
		return MediaDecision{
			TargetBitrate:         c.target,
			TargetFPS:             c.targetFPS,
			TargetResolutionScale: c.targetResolutionScale,
		}
	}
	c.targetResolutionScale = nextScale
	return MediaDecision{
		TargetBitrate:         c.target,
		TargetFPS:             c.targetFPS,
		TargetResolutionScale: c.targetResolutionScale,
		ResolutionChanged:     true,
		Changed:               true,
		Reason:                "resolution_recovery",
	}
}

func (c *Controller) shouldReduceFPS(estimate NetworkEstimate) bool {
	if c == nil || c.cfg.MinFPS >= c.cfg.MaxFPS {
		return false
	}
	return estimate.Dropped > 0 ||
		estimate.QueueDelay >= 120*time.Millisecond ||
		estimate.Loss >= 5 ||
		estimate.Jitter >= 80*time.Millisecond
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
