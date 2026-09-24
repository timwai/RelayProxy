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
