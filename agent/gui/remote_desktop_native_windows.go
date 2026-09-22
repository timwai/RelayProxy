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
	inputCh chan protocol.DesktopInputEvent

	cursorID       string
	cursorSequence uint64
	cursorState    protocol.DesktopCursorState
	cursorBitmap   desktopviewer.CursorBitmap
	baseBGRA       []byte
	presentBGRA    []byte
	frameWidth     int
	frameHeight    int
	frameStride    int

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
	ctx, cancel := context.WithCancel(context.Background())
	inputCh := make(chan protocol.DesktopInputEvent, 512)

	title := "RelayProxy Remote Desktop"
	if status.TargetName != "" {
		title += " - " + status.TargetName
	}
	native, err := desktopviewer.Open(desktopviewer.Config{
		Title:  title,
		Width:  frame.Width,
		Height: frame.Height,
		OnInput: func(event protocol.DesktopInputEvent) {
			select {
			case inputCh <- event:
			default:
				if event.Kind != protocol.DesktopInputMouseMove {
					log.Printf("[Desktop] native viewer input queue full; dropped %s", event.Kind)
				}
			}
		},
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open D3D11 viewer: %w", err)
	}

	var decoder desktopcodec.Decoder
	if device := native.D3D11Device(); device != 0 {
		decoder, err = desktopcodec.OpenMFH264DecoderWithD3D11(ctx, decoderConfig, true, device)
		if err != nil {
			log.Printf("[Desktop] shared-device H.264 decoder unavailable, falling back: %v", err)
			decoder, err = desktopcodec.OpenMFH264Decoder(ctx, decoderConfig, true)
		}
	} else {
		decoder, err = desktopcodec.OpenMFH264Decoder(ctx, decoderConfig, true)
	}
	if err != nil {
		cancel()
		_ = native.Close()
		return nil, fmt.Errorf("open Media Foundation H.264 decoder: %w", err)
	}

	session := &nativeDesktopSession{
		cancel:  cancel,
		viewer:  native,
		decoder: decoder,
		inputCh: inputCh,
		done:    make(chan struct{}),
	}

	a.desktopViewerMu.Lock()
	if existing := a.desktopViewer; existing != nil {
		a.desktopViewerMu.Unlock()
		cancel()
		_ = decoder.Close()
		_ = native.Close()
		existing.viewer.Focus()
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
		"decoder":  decoder.Backend(),
	}, nil
}

func (s *nativeDesktopSession) run(ctx context.Context, owner *appWindow) {
	defer close(s.done)
	go s.inputLoop(ctx, owner)
	defer s.viewer.Close()
	defer s.decoder.Close()
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
	var converted []byte
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

		cursorChanged := s.refreshCursor(owner)
		frame := owner.bridge.GetRemoteDesktopFrame()
		frameChanged := frame.Sequence != 0 && frame.Sequence != lastSequence && frame.MimeType == "video/h264"
		if !frameChanged {
			if cursorChanged && len(s.baseBGRA) > 0 {
				if err := s.present(); err != nil {
					log.Printf("[Desktop] native viewer cursor render failed: %v", err)
					return
				}
			}
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
		for i := range decoded {
			decodedFrame := &decoded[i]
			func() {
				defer decodedFrame.Close()
				if decodedFrame.Format != desktopcodec.PixelFormatNV12 {
					return
				}

				s.frameWidth = decodedFrame.Width
				s.frameHeight = decodedFrame.Height
				s.frameStride = decodedFrame.Width * 4

				needsCursorComposite := s.cursorState.Visible &&
					s.cursorBitmap.Width > 0 && s.cursorBitmap.Height > 0

				if decodedFrame.D3D11 != nil && !needsCursorComposite {
					s.baseBGRA = nil
					err = s.viewer.SubmitD3D11(desktopviewer.D3D11Frame{
						Resource:    decodedFrame.D3D11.Resource,
						Subresource: decodedFrame.D3D11.Subresource,
						Width:       decodedFrame.Width,
						Height:      decodedFrame.Height,
					})
					if err != nil {
						log.Printf("[Desktop] zero-copy D3D11 submit failed, falling back to readback: %v", err)
					} else {
						return
					}
				}

				nv12 := decodedFrame.Pix
				if decodedFrame.D3D11 != nil {
					nv12, err = decodedFrame.D3D11.ReadNV12()
					if err != nil {
						log.Printf("[Desktop] D3D11 surface readback failed: %v", err)
						return
					}
				}
				converted, err = desktopcodec.NV12ToBGRA(
					nv12,
					decodedFrame.Width,
					decodedFrame.Height,
					decodedFrame.Stride,
					converted,
				)
				if err != nil {
					log.Printf("[Desktop] native viewer NV12 conversion failed: %v", err)
					return
				}
				s.baseBGRA = append(s.baseBGRA[:0], converted...)
				if err = s.present(); err != nil {
					log.Printf("[Desktop] native viewer render failed: %v", err)
				}
			}()
			if err != nil {
				return
			}
		}	}
}

func (s *nativeDesktopSession) refreshCursor(owner *appWindow) bool {
	state := owner.bridge.GetRemoteDesktopCursor(s.cursorID)
	if state.Sequence == 0 || state.Sequence == s.cursorSequence {
		return false
	}
	s.cursorSequence = state.Sequence
	if state.CursorID != "" && state.CursorID != s.cursorID {
		if len(state.PNG) > 0 {
			bitmap, err := desktopviewer.DecodeCursorPNG(state.CursorID, state.PNG)
			if err != nil {
				log.Printf("[Desktop] native viewer cursor decode failed: %v", err)
			} else {
				s.cursorBitmap = bitmap
				s.cursorID = state.CursorID
			}
		}
	}
	s.cursorState = state
	return true
}

func (s *nativeDesktopSession) present() error {
	if len(s.baseBGRA) == 0 || s.frameWidth <= 0 || s.frameHeight <= 0 || s.frameStride <= 0 {
		return nil
	}
	var err error
	s.presentBGRA, err = desktopviewer.CompositeCursorBGRA(
		s.baseBGRA,
		s.frameWidth,
		s.frameHeight,
		s.frameStride,
		s.cursorState,
		s.cursorBitmap,
		s.presentBGRA,
	)
	if err != nil {
		return err
	}
	return s.viewer.Submit(desktopviewer.Frame{
		BGRA:   s.presentBGRA,
		Width:  s.frameWidth,
		Height: s.frameHeight,
		Stride: s.frameStride,
	})
}

func (s *nativeDesktopSession) inputLoop(ctx context.Context, owner *appWindow) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.inputCh:
			if err := owner.bridge.SendRemoteDesktopInput(event); err != nil && ctx.Err() == nil {
				log.Printf("[Desktop] native viewer input send failed: %v", err)
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
			"decoder":  session.decoder.Backend(),
		}
	}
}
