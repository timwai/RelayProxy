package desktop

import (
	"context"
	"log"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

const desktopDisplayRefreshInterval = 2 * time.Second

func cloneDesktopDisplays(in []protocol.DesktopDisplayCapability) []protocol.DesktopDisplayCapability {
	return append([]protocol.DesktopDisplayCapability(nil), in...)
}

func sameDesktopDisplays(a, b []protocol.DesktopDisplayCapability) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func desktopDisplayRecoveryTarget(
	cfg HostConfig,
	displays []protocol.DesktopDisplayCapability,
) (string, bool) {
	current := cfg.DisplayID
	if current == "" {
		return "", false
	}
	for _, display := range displays {
		if display.ID == current {
			return "", false
		}
	}
	switch cfg.CaptureBackend {
	case protocol.DesktopCaptureDXGI, protocol.DesktopCaptureWGC:
		if len(displays) == 0 {
			return "", false
		}
		for _, display := range displays {
			if display.Primary && display.ID != "" {
				return display.ID, true
			}
		}
		for _, display := range displays {
			if display.ID != "" {
				return display.ID, true
			}
		}
		return "", false
	default:
		return "", true
	}
}

func (h *Host) recoverMissingSessionDisplay(
	ctx context.Context,
	cfg HostConfig,
) (HostConfig, bool, error) {
	provider, ok := h.source.(CaptureCapabilitySource)
	if !ok {
		return cfg, false, nil
	}
	_, displays, err := provider.DesktopCaptureCapabilities(ctx)
	if err != nil {
		return cfg, false, err
	}
	target, recover := desktopDisplayRecoveryTarget(cfg, displays)
	if !recover {
		return cfg, false, nil
	}
	next, err := h.switchSessionDisplay(ctx, cfg, target)
	if err != nil {
		return cfg, false, err
	}
	return next, true, nil
}

func (h *Host) streamSessionDisplays(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	provider CaptureCapabilitySource,
) error {
	if h == nil || conn == nil || provider == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	var (
		last        []protocol.DesktopDisplayCapability
		initialized bool
	)
	refresh := func() error {
		_, displays, err := provider.DesktopCaptureCapabilities(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[Desktop] refresh display capabilities failed: %v", err)
			return nil
		}
		if initialized && sameDesktopDisplays(last, displays) {
			return nil
		}
		last = cloneDesktopDisplays(displays)
		initialized = true
		return conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
			Type:     protocol.DesktopSessionDisplays,
			Displays: cloneDesktopDisplays(displays),
		})
	}

	if err := refresh(); err != nil {
		return err
	}
	ticker := time.NewTicker(desktopDisplayRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}
