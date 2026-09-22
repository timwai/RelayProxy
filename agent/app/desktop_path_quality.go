package app

import (
	"context"
	"errors"
	"log"
	"net"
	"time"

	desktop "relayproxy/agent/desktop"
	rdpp2p "relayproxy/agent/rdp/p2p"
	desktopmedia "relayproxy/internal/desktop"
)

func (a *Agent) requestRelayDesktopPathIDR(controller *desktop.ControllerSession, targetID, fromPath, toPath string) {
	if a == nil || controller == nil || !controller.Active() {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Second)
	err := controller.RequestIDR(ctx)
	cancel()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
		log.Printf("[Desktop] request IDR after path change target=%s from=%s to=%s failed: %v", targetID, fromPath, toPath, err)
	}
}

// waitRelayDesktopDirectPath keeps udp_p2p under quality probation while the
// reliable session stays on Relay. When the direct media path remains worse
// than the pre-promotion Relay baseline for the configured hysteresis window,
// it is demoted before the UDP lease fails outright.
func (a *Agent) waitRelayDesktopDirectPath(
	controller *desktop.ControllerSession,
	targetID string,
	path desktopmedia.DatagramPath,
	direct *rdpp2p.Session,
	lost <-chan struct{},
	relayQuality desktop.PathQuality,
) (qualityFallback string, sessionAlive bool) {
	if a == nil || controller == nil || path == nil {
		return "", false
	}
	policy := desktop.DefaultPathScorePolicy()
	var gate desktop.PathSwitchGate
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.ctx.Done():
			return "", false
		case <-controller.Done():
			return "", false
		case <-lost:
			return "", true
		case now := <-ticker.C:
			if !relayQuality.Available {
				continue
			}
			directQuality := controller.PathQuality()
			if !directQuality.Available {
				continue
			}
			if direct != nil {
				metrics := direct.DirectPathMetrics()
				if metrics.Samples > 0 {
					directQuality.RTTMs = metrics.RTTMs
					directQuality.JitterMs = metrics.JitterMs
				}
			}
			decision := gate.Evaluate(
				now,
				path.Name(),
				directQuality,
				"relay",
				relayQuality,
				policy,
			)
			if !decision.Switch {
				continue
			}
			log.Printf(
				"[Desktop] P2P media quality fallback target=%s reason=%s direct_score=%.2f relay_score=%.2f rtt=%.2fms jitter=%.2fms loss=%.2f%% queue=%.2fms",
				targetID,
				decision.Reason,
				decision.CurrentScore,
				decision.CandidateScore,
				directQuality.RTTMs,
				directQuality.JitterMs,
				directQuality.LossPercent,
				directQuality.QueueDelayMs,
			)
			controller.ClearDatagramPath(path)
			return decision.Reason, true
		}
	}
}
