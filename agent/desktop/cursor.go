package desktop

import (
	"context"
	"errors"
	"log"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type CursorCaptureSource interface {
	CaptureCursor(context.Context) (protocol.DesktopCursorState, error)
}

func cursorStateChanged(previous, next protocol.DesktopCursorState) bool {
	return previous.X != next.X ||
		previous.Y != next.Y ||
		previous.ScreenWidth != next.ScreenWidth ||
		previous.ScreenHeight != next.ScreenHeight ||
		previous.Visible != next.Visible ||
		previous.CursorID != next.CursorID ||
		previous.Width != next.Width ||
		previous.Height != next.Height ||
		previous.HotspotX != next.HotspotX ||
		previous.HotspotY != next.HotspotY
}

func cursorUpdate(previous, next protocol.DesktopCursorState, sequence uint64) protocol.DesktopCursorState {
	next.Sequence = sequence
	if next.CursorID != "" && next.CursorID == previous.CursorID {
		next.PNG = nil
	} else {
		next.PNG = append([]byte(nil), next.PNG...)
	}
	return next
}

func (h *Host) streamCursor(ctx context.Context, conn *desktopmedia.MediaConn, source CursorCaptureSource) error {
	if source == nil {
		return errors.New("desktop cursor source is unavailable")
	}
	const interval = time.Second / 60
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var previous protocol.DesktopCursorState
	var sequence uint64
	send := func() error {
		state, err := source.CaptureCursor(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[Desktop] cursor capture failed: %v", err)
			return nil
		}
		if sequence != 0 && !cursorStateChanged(previous, state) {
			return nil
		}
		sequence++
		update := cursorUpdate(previous, state, sequence)
		if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
			Type:   protocol.DesktopSessionCursor,
			Cursor: &update,
		}); err != nil {
			return err
		}
		previous = state
		return nil
	}

	if err := send(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := send(); err != nil {
				return err
			}
		}
	}
}
