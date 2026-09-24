//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	desktop "relayproxy/agent/desktop"
	desktopaudio "relayproxy/agent/desktop/audio"
	desktopcodec "relayproxy/agent/desktop/codec"
	desktopviewer "relayproxy/agent/desktop/viewer"
	"relayproxy/internal/protocol"
)

type nativeViewerPerf struct {
	started      time.Time
	decodeFrames uint64
	renderFrames uint64
	decodeTime   time.Duration
	renderTime   time.Duration
}

func newNativeViewerPerf(now time.Time) *nativeViewerPerf {
	return &nativeViewerPerf{started: now}
}

func (p *nativeViewerPerf) observeDecode(frames int, elapsed time.Duration) {
	if p == nil || frames <= 0 {
		return
	}
	p.decodeFrames += uint64(frames)
	p.decodeTime += elapsed
}

func (p *nativeViewerPerf) observeRender(elapsed time.Duration) {
	if p == nil {
		return
	}
	p.renderFrames++
	p.renderTime += elapsed
}

func (p *nativeViewerPerf) report(now time.Time) (protocol.DesktopSessionStats, bool) {
	if p == nil {
		return protocol.DesktopSessionStats{}, false
	}
	elapsed := now.Sub(p.started)
	if elapsed < time.Second {
		return protocol.DesktopSessionStats{}, false
	}
	seconds := elapsed.Seconds()
	stats := protocol.DesktopSessionStats{}
	if p.decodeFrames > 0 {
		stats.DecodeFPS = float64(p.decodeFrames) / seconds
		stats.DecodeMs = float64(p.decodeTime.Microseconds()) / 1000 / float64(p.decodeFrames)
	}
	if p.renderFrames > 0 {
		stats.RenderFPS = float64(p.renderFrames) / seconds
		stats.RenderMs = float64(p.renderTime.Microseconds()) / 1000 / float64(p.renderFrames)
	}
	p.started = now
	p.decodeFrames = 0
	p.renderFrames = 0
	p.decodeTime = 0
	p.renderTime = 0
	return stats, true
}

type nativeDesktopSession struct {
	cancel context.CancelFunc

	sessionID         string
	disconnectOnClose bool
	persistPlacement  bool

	mediaMu       sync.RWMutex
	viewer        desktopviewer.Native
	decoder       desktopcodec.Decoder
	inputCh       chan protocol.DesktopInputEvent
	viewportCh    chan desktopviewer.Viewport
	title         string
	generation    uint32
	decoderCodec  string
	decoderWidth  int
	decoderHeight int

	cursorID       string
	cursorSequence uint64
	cursorState    protocol.DesktopCursorState
	cursorBitmap   desktopviewer.CursorBitmap
	baseBGRA       []byte
	presentBGRA    []byte
	frameWidth     int
	frameHeight    int
	frameStride    int

	gpuCursor      bool
	gpuFrameActive bool

	done      chan struct{}
	closeOnce sync.Once
}

func (s *nativeDesktopSession) sessionKey() string {
	if s == nil || strings.TrimSpace(s.sessionID) == "" {
		return "primary"
	}
	return strings.TrimSpace(s.sessionID)
}

func (s *nativeDesktopSession) status(owner *appWindow) protocol.RemoteDesktopStatus {
	if s != nil && s.sessionID != "" {
		return owner.bridge.GetRemoteDesktopStatusForSession(s.sessionID)
	}
	return owner.bridge.GetRemoteDesktopStatus()
}

func (s *nativeDesktopSession) frame(owner *appWindow) protocol.RemoteDesktopFrame {
	if s != nil && s.sessionID != "" {
		return owner.bridge.GetRemoteDesktopFrameForSession(s.sessionID)
	}
	return owner.bridge.GetRemoteDesktopFrame()
}

func (s *nativeDesktopSession) cursor(owner *appWindow, knownCursorID string) protocol.DesktopCursorState {
	if s != nil && s.sessionID != "" {
		return owner.bridge.GetRemoteDesktopCursorForSession(s.sessionID, knownCursorID)
	}
	return owner.bridge.GetRemoteDesktopCursor(knownCursorID)
}

func (s *nativeDesktopSession) requestIDR(owner *appWindow) error {
	if s != nil && s.sessionID != "" {
		return owner.bridge.RequestRemoteDesktopIDRForSession(s.sessionID)
	}
	return owner.bridge.RequestRemoteDesktopIDR()
}

func (s *nativeDesktopSession) sendInput(owner *appWindow, event protocol.DesktopInputEvent) error {
	if s != nil && s.sessionID != "" {
		return owner.bridge.SendRemoteDesktopInputForSession(s.sessionID, event)
	}
	return owner.bridge.SendRemoteDesktopInput(event)
}

func (s *nativeDesktopSession) viewportFollowEnabled(owner *appWindow) bool {
	if s != nil && s.sessionID != "" {
		return owner.bridge.RemoteDesktopViewportFollowEnabledForSession(s.sessionID)
	}
	return owner.bridge.RemoteDesktopViewportFollowEnabled()
}

func (s *nativeDesktopSession) setViewportResolution(owner *appWindow, width, height int) error {
	if s != nil && s.sessionID != "" {
		return owner.bridge.SetRemoteDesktopViewportResolutionForSession(s.sessionID, width, height)
	}
	return owner.bridge.SetRemoteDesktopViewportResolution(width, height)
}

func (s *nativeDesktopSession) reportViewerStats(owner *appWindow, stats protocol.DesktopSessionStats) {
	if s != nil && s.sessionID != "" {
		owner.bridge.ReportRemoteDesktopViewerStatsForSession(s.sessionID, stats)
		return
	}
	owner.bridge.ReportRemoteDesktopViewerStats(stats)
}

func (s *nativeDesktopSession) audioEnabled(owner *appWindow) bool {
	if s != nil && s.sessionID != "" {
		return owner.bridge.RemoteDesktopAudioEnabledForSession(s.sessionID)
	}
	return owner.bridge.RemoteDesktopAudioEnabled()
}

func (s *nativeDesktopSession) nextAudioFrame(
	ctx context.Context,
	owner *appWindow,
) (desktop.AudioFrameSnapshot, protocol.DesktopAudioConfig, error) {
	if s != nil && s.sessionID != "" {
		return owner.bridge.NextRemoteDesktopAudioFrameForSession(ctx, s.sessionID)
	}
	return owner.bridge.NextRemoteDesktopAudioFrame(ctx)
}

func queueLatestNativeViewport(ch chan desktopviewer.Viewport, viewport desktopviewer.Viewport) {
	if ch == nil || !viewport.Valid() {
		return
	}
	select {
	case ch <- viewport:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- viewport:
	default:
	}
}

func nativeDesktopViewerConfig(
	title string,
	width, height int,
	inputCh chan protocol.DesktopInputEvent,
	viewportCh chan desktopviewer.Viewport,
	viewport desktopviewer.Viewport,
	placement desktopviewer.WindowPlacement,
) desktopviewer.Config {
	return desktopviewer.Config{
		Title:          title,
		Width:          width,
		Height:         height,
		ViewportWidth:  viewport.Width,
		ViewportHeight: viewport.Height,
		Placement:      placement,
		OnInput: func(event protocol.DesktopInputEvent) {
			select {
			case inputCh <- event:
			default:
				if event.Kind != protocol.DesktopInputMouseMove {
					log.Printf("[Desktop] native viewer input queue full; dropped %s", event.Kind)
				}
			}
		},
		OnViewport: func(viewport desktopviewer.Viewport) {
			queueLatestNativeViewport(viewportCh, viewport)
		},
	}
}

func nativeDesktopFrameCodec(frame protocol.RemoteDesktopFrame) string {
	switch frame.MimeType {
	case "video/h264":
		return "h264"
	case "video/h265":
		return "h265"
	default:
		return ""
	}
}

func nativeDesktopVideoFrame(frame protocol.RemoteDesktopFrame) bool {
	return nativeDesktopFrameCodec(frame) != ""
}

func nativeDesktopViewportResolution(
	viewport desktopviewer.Viewport,
	status protocol.RemoteDesktopStatus,
) (int, int, bool) {
	if !viewport.Valid() || status.Width <= 0 || status.Height <= 0 {
		return 0, 0, false
	}
	maxWidth := status.MaxWidth
	if maxWidth <= 0 {
		maxWidth = status.Width
	}
	maxHeight := status.MaxHeight
	if maxHeight <= 0 {
		maxHeight = status.Height
	}
	if maxWidth <= 0 || maxHeight <= 0 {
		return 0, 0, false
	}

	limitWidth := viewport.Width
	if limitWidth > maxWidth {
		limitWidth = maxWidth
	}
	limitHeight := viewport.Height
	if limitHeight > maxHeight {
		limitHeight = maxHeight
	}
	aspect := float64(status.Width) / float64(status.Height)
	width := limitWidth
	height := int(float64(width) / aspect)
	if height > limitHeight {
		height = limitHeight
		width = int(float64(height) * aspect)
	}
	width &^= 1
	height &^= 1
	if width < 320 || height < 180 {
		return 0, 0, false
	}
	return width, height, true
}

func nativeDesktopViewportMayGrow(currentWidth, currentHeight, lastWidth, lastHeight int) bool {
	if lastWidth <= 0 || lastHeight <= 0 {
		return false
	}
	return absInt(currentWidth-lastWidth) <= 2 && absInt(currentHeight-lastHeight) <= 2
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func openNativeDesktopDecoder(
	ctx context.Context,
	native desktopviewer.Native,
	codec string,
	width, height int,
) (desktopcodec.Decoder, error) {
	decoderConfig := desktopcodec.VideoConfig{
		Width:         width,
		Height:        height,
		FPS:           30,
		TargetBitrate: 6_000_000,
		KeyframeEvery: 2 * time.Second,
	}
	var (
		openShared func(context.Context, desktopcodec.VideoConfig, bool, uintptr) (desktopcodec.Decoder, error)
		openCPU    func(context.Context, desktopcodec.VideoConfig, bool) (desktopcodec.Decoder, error)
	)
	switch codec {
	case "h264":
		openShared = desktopcodec.OpenMFH264DecoderWithD3D11
		openCPU = desktopcodec.OpenMFH264Decoder
	case "h265":
		openShared = desktopcodec.OpenMFH265DecoderWithD3D11
		openCPU = desktopcodec.OpenMFH265Decoder
	default:
		return nil, fmt.Errorf("unsupported native Relay Desktop codec %q", codec)
	}

	var decoder desktopcodec.Decoder
	var err error
	if device := native.D3D11Device(); device != 0 {
		decoder, err = openShared(ctx, decoderConfig, true, device)
		if err != nil {
			log.Printf("[Desktop] shared-device %s decoder unavailable, falling back: %v", codec, err)
			decoder, err = openCPU(ctx, decoderConfig, true)
		}
	} else {
		decoder, err = openCPU(ctx, decoderConfig, true)
	}
	return decoder, err
}

func nativeDesktopFrameNeedsRebuild(
	generation uint32,
	codec string,
	width, height int,
	frame protocol.RemoteDesktopFrame,
) bool {
	return frame.Generation != generation ||
		nativeDesktopFrameCodec(frame) != codec ||
		frame.Width != width ||
		frame.Height != height
}

func (s *nativeDesktopSession) focusViewer() {
	if s == nil {
		return
	}
	s.mediaMu.RLock()
	defer s.mediaMu.RUnlock()
	if s.viewer != nil {
		s.viewer.Focus()
	}
}

func (s *nativeDesktopSession) disableGPUCursor() {
	if s == nil {
		return
	}
	s.mediaMu.Lock()
	s.gpuCursor = false
	s.mediaMu.Unlock()
}

func (s *nativeDesktopSession) decoderStatus() (string, bool, bool) {
	if s == nil {
		return "", false, false
	}
	s.mediaMu.RLock()
	defer s.mediaMu.RUnlock()
	if s.decoder == nil {
		return "", false, false
	}
	return s.decoder.Backend(), s.decoder.Hardware(), s.gpuCursor
}

func (s *nativeDesktopSession) rebuildMediaPipeline(ctx context.Context, frame protocol.RemoteDesktopFrame) error {
	if s == nil || frame.Width <= 0 || frame.Height <= 0 {
		return errors.New("invalid Relay Desktop media generation")
	}
	codec := nativeDesktopFrameCodec(frame)
	if codec == "" {
		return fmt.Errorf("unsupported Relay Desktop video MIME type %q", frame.MimeType)
	}
	s.mediaMu.RLock()
	currentViewer := s.viewer
	oldWidth := s.decoderWidth
	oldHeight := s.decoderHeight
	sameSize := oldWidth == frame.Width && oldHeight == frame.Height
	s.mediaMu.RUnlock()
	if currentViewer == nil {
		return errors.New("native Relay Desktop viewer is unavailable")
	}

	if !sameSize {
		if err := currentViewer.Reconfigure(frame.Width, frame.Height); err != nil {
			return fmt.Errorf("reconfigure D3D11 viewer for generation %d: %w", frame.Generation, err)
		}
	}
	nextDecoder, err := openNativeDesktopDecoder(ctx, currentViewer, codec, frame.Width, frame.Height)
	if err != nil {
		if !sameSize {
			if rollbackErr := currentViewer.Reconfigure(oldWidth, oldHeight); rollbackErr != nil {
				log.Printf("[Desktop] native viewer rollback to %dx%d failed after decoder error: %v", oldWidth, oldHeight, rollbackErr)
			}
		}
		return fmt.Errorf("reopen %s decoder for generation %d: %w", codec, frame.Generation, err)
	}

	s.mediaMu.Lock()
	oldDecoder := s.decoder
	s.decoder = nextDecoder
	s.generation = frame.Generation
	s.decoderCodec = codec
	s.decoderWidth = frame.Width
	s.decoderHeight = frame.Height
	s.gpuCursor = currentViewer.SupportsGPUCursor() && nextDecoder.Backend() == "media-foundation-d3d11-zero-copy"
	s.gpuFrameActive = false
	s.baseBGRA = nil
	s.presentBGRA = nil
	s.frameWidth = 0
	s.frameHeight = 0
	s.frameStride = 0
	s.mediaMu.Unlock()

	if oldDecoder != nil {
		_ = oldDecoder.Close()
	}
	return nil
}

func (a *appWindow) openNativeDesktopViewer() (map[string]any, error) {
	if a == nil || a.bridge == nil {
		return nil, errors.New("GUI unavailable")
	}
	status := a.bridge.GetRemoteDesktopStatus()
	if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
		return nil, errors.New("Relay Desktop session is not connected")
	}
	return a.openNativeDesktopViewerForSession(status.SessionID, true, false)
}

func (a *appWindow) openNativeDesktopViewerForSession(
	sessionID string,
	primary bool,
	disconnectOnClose bool,
) (map[string]any, error) {
	if a == nil || a.bridge == nil {
		return nil, errors.New("GUI unavailable")
	}
	sessionID = strings.TrimSpace(sessionID)
	key := sessionID
	if key == "" {
		key = "primary"
	}

	a.desktopViewerMu.Lock()
	if a.desktopViewers == nil {
		a.desktopViewers = make(map[string]*nativeDesktopSession)
	}
	if existing := a.desktopViewers[key]; existing != nil {
		a.desktopViewerMu.Unlock()
		existing.focusViewer()
		return map[string]any{"ok": true, "alreadyOpen": true, "sessionId": sessionID}, nil
	}
	if primary && a.desktopViewer != nil {
		existing := a.desktopViewer
		a.desktopViewerMu.Unlock()
		existing.focusViewer()
		return map[string]any{"ok": true, "alreadyOpen": true, "sessionId": existing.sessionID}, nil
	}
	a.desktopViewerMu.Unlock()

	var status protocol.RemoteDesktopStatus
	var frame protocol.RemoteDesktopFrame
	if sessionID != "" {
		status = a.bridge.GetRemoteDesktopStatusForSession(sessionID)
		frame = a.bridge.GetRemoteDesktopFrameForSession(sessionID)
	} else {
		status = a.bridge.GetRemoteDesktopStatus()
		frame = a.bridge.GetRemoteDesktopFrame()
	}
	if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
		return nil, errors.New("Relay Desktop session is not connected")
	}
	if sessionID == "" {
		sessionID = status.SessionID
		if sessionID != "" {
			key = sessionID
		}
	}
	codec := nativeDesktopFrameCodec(frame)
	if frame.Sequence == 0 || codec == "" || frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
		return nil, errors.New("H.264/H.265 frame is not ready; wait for the remote picture and retry")
	}

	ctx, cancel := context.WithCancel(context.Background())
	inputCh := make(chan protocol.DesktopInputEvent, 512)
	viewportCh := make(chan desktopviewer.Viewport, 1)

	title := "RelayProxy Remote Desktop"
	if status.TargetName != "" {
		title += " - " + status.TargetName
	}
	if status.DisplayName != "" {
		title += " - " + status.DisplayName
	}

	placement := desktopviewer.WindowPlacement{}
	if primary {
		savedPlacement := a.bridge.GetConfig().GUI.NativeViewer
		placement = desktopviewer.WindowPlacement{
			X: savedPlacement.X, Y: savedPlacement.Y,
			Width: savedPlacement.Width, Height: savedPlacement.Height,
			Maximized: savedPlacement.Maximized,
		}
	}
	native, err := desktopviewer.Open(nativeDesktopViewerConfig(
		title,
		frame.Width,
		frame.Height,
		inputCh,
		viewportCh,
		desktopviewer.Viewport{},
		placement,
	))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open D3D11 viewer: %w", err)
	}

	decoder, err := openNativeDesktopDecoder(ctx, native, codec, frame.Width, frame.Height)
	if err != nil {
		cancel()
		_ = native.Close()
		return nil, fmt.Errorf("open Media Foundation %s decoder: %w", codec, err)
	}

	session := &nativeDesktopSession{
		cancel:            cancel,
		sessionID:         sessionID,
		disconnectOnClose: disconnectOnClose,
		persistPlacement:  primary,
		viewer:            native,
		decoder:           decoder,
		inputCh:           inputCh,
		viewportCh:        viewportCh,
		title:             title,
		generation:        frame.Generation,
		decoderCodec:      codec,
		decoderWidth:      frame.Width,
		decoderHeight:     frame.Height,
		gpuCursor:         native.SupportsGPUCursor() && decoder.Backend() == "media-foundation-d3d11-zero-copy",
		done:              make(chan struct{}),
	}

	a.desktopViewerMu.Lock()
	if a.desktopViewers == nil {
		a.desktopViewers = make(map[string]*nativeDesktopSession)
	}
	if existing := a.desktopViewers[key]; existing != nil || (primary && a.desktopViewer != nil) {
		if existing == nil {
			existing = a.desktopViewer
		}
		a.desktopViewerMu.Unlock()
		cancel()
		_ = decoder.Close()
		_ = native.Close()
		if existing != nil {
			existing.focusViewer()
		}
		return map[string]any{"ok": true, "alreadyOpen": true, "sessionId": sessionID}, nil
	}
	a.desktopViewers[key] = session
	if primary {
		a.desktopViewer = session
	}
	a.desktopViewerMu.Unlock()

	go session.run(ctx, a)
	return map[string]any{
		"ok":        true,
		"sessionId": sessionID,
		"width":     frame.Width,
		"height":    frame.Height,
		"hardware":  decoder.Hardware(),
		"decoder":   decoder.Backend(),
		"gpuCursor": session.gpuCursor,
	}, nil
}

func (s *nativeDesktopSession) run(ctx context.Context, owner *appWindow) {
	defer close(s.done)
	defer s.cancel()
	go s.inputLoop(ctx, owner)
	go s.viewportLoop(ctx, owner)
	if s.audioEnabled(owner) {
		go s.audioLoop(ctx, owner)
	}
	defer func() {
		s.mediaMu.RLock()
		viewer := s.viewer
		decoder := s.decoder
		s.mediaMu.RUnlock()
		if decoder != nil {
			_ = decoder.Close()
		}
		if viewer != nil {
			placement := viewer.WindowPlacement()
			_ = viewer.Close()
			if s.persistPlacement && placement.Valid() {
				if err := owner.persistGUI(map[string]any{
					"nativeViewer": map[string]any{
						"x": placement.X, "y": placement.Y,
						"width": placement.Width, "height": placement.Height,
						"maximized": placement.Maximized,
					},
				}); err != nil {
					log.Printf("[Desktop] persist native viewer placement failed: %v", err)
				}
			}
		}
	}()
	defer func() {
		owner.desktopViewerMu.Lock()
		if owner.desktopViewer == s {
			owner.desktopViewer = nil
		}
		key := s.sessionKey()
		if owner.desktopViewers[key] == s {
			delete(owner.desktopViewers, key)
		}
		owner.desktopViewerMu.Unlock()
		if s.disconnectOnClose && s.sessionID != "" {
			owner.bridge.DisconnectRemoteDesktopSession(s.sessionID)
		}
	}()

	ticker := time.NewTicker(8 * time.Millisecond)
	defer ticker.Stop()

	var lastSequence uint64
	var lastGeneration = s.generation
	var converted []byte
	var lastRecovery time.Time
	perf := newNativeViewerPerf(time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.viewer.Done():
			return
		case <-ticker.C:
		}

		status := s.status(owner)
		if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
			return
		}

		cursorChanged := s.refreshCursor(owner)
		frame := s.frame(owner)
		frameChanged := frame.Sequence != 0 && nativeDesktopVideoFrame(frame) &&
			(frame.Sequence != lastSequence || frame.Generation != lastGeneration)
		if !frameChanged {
			if cursorChanged && !s.gpuFrameActive && len(s.baseBGRA) > 0 {
				if err := s.present(); err != nil {
					log.Printf("[Desktop] native viewer cursor render failed: %v", err)
					return
				}
			}
			continue
		}
		lastSequence = frame.Sequence
		lastGeneration = frame.Generation
		if frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
			continue
		}

		if nativeDesktopFrameNeedsRebuild(s.generation, s.decoderCodec, s.decoderWidth, s.decoderHeight, frame) {
			if !frame.KeyFrame {
				if time.Since(lastRecovery) >= 500*time.Millisecond {
					lastRecovery = time.Now()
					if requestErr := s.requestIDR(owner); requestErr != nil {
						log.Printf("[Desktop] native viewer generation IDR request failed: %v", requestErr)
					}
				}
				continue
			}
			if err := s.rebuildMediaPipeline(ctx, frame); err != nil {
				log.Printf("[Desktop] native viewer generation rebuild failed: %v", err)
				if time.Since(lastRecovery) >= 500*time.Millisecond {
					lastRecovery = time.Now()
					_ = s.requestIDR(owner)
				}
				continue
			}
			converted = nil
			perf = newNativeViewerPerf(time.Now())
			log.Printf("[Desktop] native viewer switched generation=%d codec=%s size=%dx%d decoder=%s", frame.Generation, s.decoderCodec, frame.Width, frame.Height, s.decoder.Backend())
		}

		decodeStarted := time.Now()
		decoded, err := s.decoder.Decode(ctx, frame.Data, time.Duration(frame.Timestamp)*time.Microsecond)
		decodeElapsed := time.Since(decodeStarted)
		if err != nil {
			if time.Since(lastRecovery) >= 500*time.Millisecond {
				lastRecovery = time.Now()
				if requestErr := s.requestIDR(owner); requestErr != nil {
					log.Printf("[Desktop] native viewer IDR request failed: %v", requestErr)
				}
			}
			_ = s.decoder.Flush(ctx)
			log.Printf("[Desktop] native viewer %s decode failed: %v", s.decoderCodec, err)
			continue
		}
		perf.observeDecode(len(decoded), decodeElapsed)
		for i := range decoded {
			decodedFrame := &decoded[i]
			func() {
				defer decodedFrame.Close()
				renderStarted := time.Now()
				rendered := false
				defer func() {
					if rendered {
						perf.observeRender(time.Since(renderStarted))
					}
				}()
				if decodedFrame.Format != desktopcodec.PixelFormatNV12 {
					return
				}

				s.frameWidth = decodedFrame.Width
				s.frameHeight = decodedFrame.Height
				s.frameStride = decodedFrame.Width * 4

				needsCursorComposite := s.cursorState.Visible &&
					s.cursorBitmap.Width > 0 && s.cursorBitmap.Height > 0 &&
					!s.gpuCursor

				if decodedFrame.D3D11 != nil && !needsCursorComposite {
					if s.gpuCursor {
						if cursorErr := s.viewer.SetCursor(desktopviewer.CursorOverlay{
							State: s.cursorState, Bitmap: s.cursorBitmap,
						}); cursorErr != nil {
							log.Printf("[Desktop] GPU cursor update failed: %v", cursorErr)
							s.disableGPUCursor()
							if s.cursorState.Visible {
								needsCursorComposite = true
							}
						}
					}
					if !needsCursorComposite {
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
							s.gpuFrameActive = true
							rendered = true
							return
						}
					}
				}

				s.gpuFrameActive = false
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
				} else {
					rendered = true
				}
			}()
			if err != nil {
				return
			}
		}
		if stats, ok := perf.report(time.Now()); ok {
			stats.DecoderBackend = s.decoder.Backend()
			stats.DecoderHardware = s.decoder.Hardware()
			owner.bridge.ReportRemoteDesktopViewerStats(stats)
		}
	}
}

func (s *nativeDesktopSession) viewportLoop(ctx context.Context, owner *appWindow) {
	if s == nil || owner == nil || owner.bridge == nil || s.viewportCh == nil {
		return
	}
	const (
		debounce = 300 * time.Millisecond
		retry    = 2 * time.Second
	)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var (
		viewport        desktopviewer.Viewport
		viewportChanged time.Time
		lastWidth       int
		lastHeight      int
		lastRequest     time.Time
	)
	for {
		select {
		case <-ctx.Done():
			return
		case next := <-s.viewportCh:
			if next.Valid() {
				viewport = next
				viewportChanged = time.Now()
			}
		case now := <-ticker.C:
			if !viewport.Valid() || viewportChanged.IsZero() || now.Sub(viewportChanged) < debounce {
				continue
			}
			if !s.viewportFollowEnabled(owner) {
				continue
			}
			status := s.status(owner)
			if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay ||
				(status.Codec != "h264" && status.Codec != "h265") {
				continue
			}
			width, height, ok := nativeDesktopViewportResolution(viewport, status)
			if !ok {
				continue
			}
			currentWidth, currentHeight := status.Width, status.Height
			if absInt(currentWidth-width) <= 2 && absInt(currentHeight-height) <= 2 {
				lastWidth, lastHeight = width, height
				lastRequest = time.Time{}
				continue
			}

			shrink := currentWidth > width+2 || currentHeight > height+2
			grow := (currentWidth < width-2 || currentHeight < height-2) &&
				nativeDesktopViewportMayGrow(currentWidth, currentHeight, lastWidth, lastHeight)
			if !shrink && !grow {
				continue
			}
			if width == lastWidth && height == lastHeight &&
				!lastRequest.IsZero() && now.Sub(lastRequest) < retry {
				continue
			}
			if err := s.setViewportResolution(owner, width, height); err != nil {
				if lastRequest.IsZero() || now.Sub(lastRequest) >= retry {
					log.Printf("[Desktop] native viewer viewport resolution %dx%d failed: %v", width, height, err)
				}
				lastRequest = now
				continue
			}
			lastWidth, lastHeight = width, height
			lastRequest = now
			log.Printf("[Desktop] native viewer viewport=%dx%d requested media=%dx%d", viewport.Width, viewport.Height, width, height)
		}
	}
}

func (s *nativeDesktopSession) refreshCursor(owner *appWindow) bool {
	state := s.cursor(owner, s.cursorID)
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
	if s.gpuCursor && s.gpuFrameActive {
		if err := s.viewer.SetCursor(desktopviewer.CursorOverlay{
			State: s.cursorState, Bitmap: s.cursorBitmap,
		}); err != nil {
			log.Printf("[Desktop] GPU cursor redraw failed: %v", err)
			s.disableGPUCursor()
			s.gpuFrameActive = false
		}
	}
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

func (s *nativeDesktopSession) audioLoop(ctx context.Context, owner *appWindow) {
	var (
		player       desktopaudio.Player
		opusDecoder  *desktopaudio.OpusDecoder
		activeConfig protocol.DesktopAudioConfig
	)
	defer func() {
		if player != nil {
			_ = player.Close()
		}
	}()

	for {
		frame, config, err := s.nextAudioFrame(ctx, owner)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[Desktop] native audio stream stopped: %v", err)
			}
			return
		}

		pcm := desktopaudio.PCMConfig{
			SampleRate:    config.SampleRate,
			Channels:      config.Channels,
			BitsPerSample: config.BitsPerSample,
		}
		if player == nil || activeConfig != config {
			if player != nil {
				_ = player.Close()
				player = nil
			}
			opusDecoder = nil

			switch config.Codec {
			case protocol.DesktopAudioCodecPCMS16LE:
			case protocol.DesktopAudioCodecOpus:
				nextDecoder, decodeErr := desktopaudio.NewOpusDecoder(desktopaudio.OpusConfig{
					SampleRate:      config.SampleRate,
					Channels:        config.Channels,
					BitsPerSample:   config.BitsPerSample,
					FrameDurationMs: config.FrameDurationMs,
					Bitrate:         config.TargetBitrate,
				})
				if decodeErr != nil {
					log.Printf("[Desktop] create native Opus decoder failed: %v", decodeErr)
					return
				}
				opusDecoder = nextDecoder
			default:
				log.Printf("[Desktop] native audio codec %q is not supported", config.Codec)
				return
			}

			next, openErr := desktopaudio.OpenPCMPlayer(ctx, pcm)
			if openErr != nil {
				if ctx.Err() == nil {
					log.Printf("[Desktop] open native WASAPI audio player failed: %v", openErr)
				}
				return
			}
			player = next
			activeConfig = config
			log.Printf("[Desktop] native audio generation=%d codec=%s format=%dHz/%dch/%dbit bitrate=%d",
				config.Generation, config.Codec, config.SampleRate, config.Channels, config.BitsPerSample, config.TargetBitrate)
		}
		var pcmData []byte
		switch {
		case frame.Concealment:
			if opusDecoder == nil {
				continue
			}
			pcmData, err = opusDecoder.DecodePLC()
			if err != nil {
				log.Printf("[Desktop] Opus PLC failed generation=%d frame=%d: %v",
					frame.Generation, frame.FrameID, err)
				continue
			}
		case len(frame.Data) == 0:
			continue
		case opusDecoder != nil:
			pcmData, err = opusDecoder.DecodePacket(frame.Data)
			if err != nil {
				log.Printf("[Desktop] Opus decode failed generation=%d frame=%d: %v",
					frame.Generation, frame.FrameID, err)
				continue
			}
		default:
			pcmData = frame.Data
		}
		if validateErr := pcm.ValidatePayload(pcmData); validateErr != nil {
			log.Printf("[Desktop] invalid native PCM frame generation=%d frame=%d: %v",
				frame.Generation, frame.FrameID, validateErr)
			continue
		}
		if writeErr := player.Write(ctx, pcmData); writeErr != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[Desktop] native WASAPI audio write failed: %v", writeErr)
			_ = player.Close()
			player = nil
			opusDecoder = nil
			activeConfig = protocol.DesktopAudioConfig{}
		}
	}
}

func (s *nativeDesktopSession) inputLoop(ctx context.Context, owner *appWindow) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-s.inputCh:
			if err := s.sendInput(owner, event); err != nil && ctx.Err() == nil {
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
	seen := make(map[*nativeDesktopSession]struct{})
	sessions := make([]*nativeDesktopSession, 0, len(a.desktopViewers)+1)
	add := func(session *nativeDesktopSession) {
		if session == nil {
			return
		}
		if _, ok := seen[session]; ok {
			return
		}
		seen[session] = struct{}{}
		sessions = append(sessions, session)
	}
	add(a.desktopViewer)
	for _, session := range a.desktopViewers {
		add(session)
	}
	a.desktopViewer = nil
	a.desktopViewers = make(map[string]*nativeDesktopSession)
	a.desktopViewerMu.Unlock()

	for _, session := range sessions {
		session.closeOnce.Do(func() {
			session.cancel()
		})
	}
	for _, session := range sessions {
		<-session.done
	}
}


func (a *appWindow) nativeDesktopViewerStatus() map[string]any {
	if a == nil {
		return map[string]any{"open": false, "windows": 0}
	}
	a.desktopViewerMu.Lock()
	session := a.desktopViewer
	windows := len(a.desktopViewers)
	a.desktopViewerMu.Unlock()
	if session == nil {
		return map[string]any{"open": false, "windows": windows}
	}
	select {
	case <-session.done:
		return map[string]any{"open": false, "windows": windows}
	default:
		decoder, hardware, gpuCursor := session.decoderStatus()
		return map[string]any{
			"open":      true,
			"windows":   windows,
			"sessionId": session.sessionID,
			"hardware":  hardware,
			"decoder":   decoder,
			"gpuCursor": gpuCursor,
		}
	}
}
