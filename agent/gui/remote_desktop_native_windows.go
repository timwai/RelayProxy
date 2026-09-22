//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopviewer "relayproxy/agent/desktop/viewer"
	"relayproxy/internal/protocol"
)

type nativeDesktopSession struct {
	cancel context.CancelFunc

	viewer  desktopviewer.Native
	decoder desktopcodec.Decoder

	done      chan struct{}
	closeOnce sync.Once
}

func (a *appWindow) openNativeDesktopViewer() (map[string]any, error) {
	if a == nil || a.bridge == nil {
		return nil, errors.New("GUI unavailable")
	}
	a.desktopViewerMu.Lock()
	if existing := a.desktopViewer; existing != nil {
		a.desktopViewerMu.Unlock()
		existing.viewer.Focus()
		return map[string]any{"ok": true, "alreadyOpen": true}, nil
	}
	a.desktopViewerMu.Unlock()

	status := a.bridge.GetRemoteDesktopStatus()
	if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
		return nil, errors.New("Relay Desktop session is not connected")
	}
	frame := a.bridge.GetRemoteDesktopFrame()
	if frame.Sequence == 0 || frame.MimeType != "video/h264" || frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
		return nil, errors.New("H.264 frame is not ready; wait for the remote picture and retry")
	}

	decoderConfig := desktopcodec.VideoConfig{
		Width:         frame.Width,
		Height:        frame.Height,
		FPS:           30,
		TargetBitrate: 6_000_000,
		KeyframeEvery: 2 * time.Second,
	}
	decoder, err := desktopcodec.OpenMFH264Decoder(context.Background(), decoderConfig, true)
	if err != nil {
		return nil, fmt.Errorf("open Media Foundation H.264 decoder: %w", err)
	}

	title := "RelayProxy Remote Desktop"
	if status.TargetName != "" {
		title += " - " + status.TargetName
	}
	native, err := desktopviewer.Open(desktopviewer.Config{
		Title:  title,
		Width:  frame.Width,
		Height: frame.Height,
	})
	if err != nil {
		_ = decoder.Close()
		return nil, fmt.Errorf("open D3D11 viewer: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &nativeDesktopSession{
		cancel:  cancel,
		viewer:  native,
		decoder: decoder,
		done:    make(chan struct{}),
	}

	a.desktopViewerMu.Lock()
	if a.desktopViewer != nil {
		a.desktopViewerMu.Unlock()
		cancel()
		_ = native.Close()
		_ = decoder.Close()
		a.desktopViewer.viewer.Focus()
		return map[string]any{"ok": true, "alreadyOpen": true}, nil
	}
	a.desktopViewer = session
	a.desktopViewerMu.Unlock()

	go session.run(ctx, a)
	return map[string]any{
		"ok":       true,
		"width":    frame.Width,
		"height":   frame.Height,
		"hardware": decoder.Hardware(),
	}, nil
}

func (s *nativeDesktopSession) run(ctx context.Context, owner *appWindow) {
	defer close(s.done)
	defer s.decoder.Close()
	defer s.viewer.Close()
	defer func() {
		owner.desktopViewerMu.Lock()
		if owner.desktopViewer == s {
			owner.desktopViewer = nil
		}
		owner.desktopViewerMu.Unlock()
	}()

	ticker := time.NewTicker(8 * time.Millisecond)
	defer ticker.Stop()

	var lastSequence uint64
	var bgra []byte
	var lastRecovery time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.viewer.Done():
			return
		case <-ticker.C:
		}

		status := owner.bridge.GetRemoteDesktopStatus()
		if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
			return
		}
		frame := owner.bridge.GetRemoteDesktopFrame()
		if frame.Sequence == 0 || frame.Sequence == lastSequence || frame.MimeType != "video/h264" {
			continue
		}
		lastSequence = frame.Sequence
		if frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
			continue
		}

		decoded, err := s.decoder.Decode(ctx, frame.Data, time.Duration(frame.Timestamp)*time.Microsecond)
		if err != nil {
			if time.Since(lastRecovery) >= 500*time.Millisecond {
				lastRecovery = time.Now()
				if requestErr := owner.bridge.RequestRemoteDesktopIDR(); requestErr != nil {
					log.Printf("[Desktop] native viewer IDR request failed: %v", requestErr)
				}
			}
			_ = s.decoder.Flush(ctx)
			log.Printf("[Desktop] native viewer H.264 decode failed: %v", err)
			continue
		}
		for _, decodedFrame := range decoded {
			if decodedFrame.Format != desktopcodec.PixelFormatNV12 {
				continue
			}
			bgra, err = desktopcodec.NV12ToBGRA(
				decodedFrame.Pix,
				decodedFrame.Width,
				decodedFrame.Height,
				decodedFrame.Stride,
				bgra,
			)
			if err != nil {
				log.Printf("[Desktop] native viewer NV12 conversion failed: %v", err)
				continue
			}
			if err := s.viewer.Submit(desktopviewer.Frame{
				BGRA:   bgra,
				Width:  decodedFrame.Width,
				Height: decodedFrame.Height,
				Stride: decodedFrame.Width * 4,
			}); err != nil {
				log.Printf("[Desktop] native viewer render failed: %v", err)
				return
			}
		}
	}
}

func (a *appWindow) stopNativeDesktopViewer() {
	if a == nil {
		return
	}
	a.desktopViewerMu.Lock()
	session := a.desktopViewer
	a.desktopViewer = nil
	a.desktopViewerMu.Unlock()
	if session == nil {
		return
	}
	session.closeOnce.Do(func() {
		session.cancel()
		_ = session.viewer.Close()
	})
	<-session.done
}

func (a *appWindow) nativeDesktopViewerStatus() map[string]any {
	if a == nil {
		return map[string]any{"open": false}
	}
	a.desktopViewerMu.Lock()
	session := a.desktopViewer
	a.desktopViewerMu.Unlock()
	if session == nil {
		return map[string]any{"open": false}
	}
	select {
	case <-session.done:
		return map[string]any{"open": false}
	default:
		return map[string]any{
			"open":     true,
			"hardware": session.decoder.Hardware(),
		}
	}
}
