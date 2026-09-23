//go:build windows

package gui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

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

	mediaMu       sync.RWMutex
	viewer        desktopviewer.Native
	decoder       desktopcodec.Decoder
	inputCh       chan protocol.DesktopInputEvent
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

func nativeDesktopViewerConfig(title string, width, height int, inputCh chan protocol.DesktopInputEvent) desktopviewer.Config {
	return desktopviewer.Config{
		Title:  title,
		Width:  width,
		Height: height,
		OnInput: func(event protocol.DesktopInputEvent) {
			select {
			case inputCh <- event:
			default:
				if event.Kind != protocol.DesktopInputMouseMove {
					log.Printf("[Desktop] native viewer input queue full; dropped %s", event.Kind)
				}
			}
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
	sameSize := s.decoderWidth == frame.Width && s.decoderHeight == frame.Height
	title := s.title
	inputCh := s.inputCh
	s.mediaMu.RUnlock()

	var (
		nextViewer  = currentViewer
		nextDecoder desktopcodec.Decoder
		err         error
	)
	if !sameSize {
		nextViewer, err = desktopviewer.Open(nativeDesktopViewerConfig(title, frame.Width, frame.Height, inputCh))
		if err != nil {
			return fmt.Errorf("reopen D3D11 viewer for generation %d: %w", frame.Generation, err)
		}
	}
	nextDecoder, err = openNativeDesktopDecoder(ctx, nextViewer, codec, frame.Width, frame.Height)
	if err != nil {
		if !sameSize && nextViewer != nil {
			_ = nextViewer.Close()
		}
		return fmt.Errorf("reopen %s decoder for generation %d: %w", codec, frame.Generation, err)
	}

	s.mediaMu.Lock()
	oldViewer := s.viewer
	oldDecoder := s.decoder
	s.viewer = nextViewer
	s.decoder = nextDecoder
	s.generation = frame.Generation
	s.decoderCodec = codec
	s.decoderWidth = frame.Width
	s.decoderHeight = frame.Height
	s.gpuCursor = nextViewer.SupportsGPUCursor() && nextDecoder.Backend() == "media-foundation-d3d11-zero-copy"
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
	if !sameSize && oldViewer != nil {
		_ = oldViewer.Close()
	}
	if !sameSize {
		nextViewer.Focus()
	}
	return nil
}

func (a *appWindow) openNativeDesktopViewer() (map[string]any, error) {
	if a == nil || a.bridge == nil {
		return nil, errors.New("GUI unavailable")
	}
	a.desktopViewerMu.Lock()
	if existing := a.desktopViewer; existing != nil {
		a.desktopViewerMu.Unlock()
		existing.focusViewer()
		return map[string]any{"ok": true, "alreadyOpen": true}, nil
	}
	a.desktopViewerMu.Unlock()

	status := a.bridge.GetRemoteDesktopStatus()
	if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
		return nil, errors.New("Relay Desktop session is not connected")
	}
	frame := a.bridge.GetRemoteDesktopFrame()
	codec := nativeDesktopFrameCodec(frame)
	if frame.Sequence == 0 || codec == "" || frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) == 0 {
		return nil, errors.New("H.264/H.265 frame is not ready; wait for the remote picture and retry")
	}

	ctx, cancel := context.WithCancel(context.Background())
	inputCh := make(chan protocol.DesktopInputEvent, 512)

	title := "RelayProxy Remote Desktop"
	if status.TargetName != "" {
		title += " - " + status.TargetName
	}
	native, err := desktopviewer.Open(nativeDesktopViewerConfig(title, frame.Width, frame.Height, inputCh))
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
		cancel:        cancel,
		viewer:        native,
		decoder:       decoder,
		inputCh:       inputCh,
		title:         title,
		generation:    frame.Generation,
		decoderCodec:  codec,
		decoderWidth:  frame.Width,
		decoderHeight: frame.Height,
		gpuCursor:     native.SupportsGPUCursor() && decoder.Backend() == "media-foundation-d3d11-zero-copy",
		done:          make(chan struct{}),
	}

	a.desktopViewerMu.Lock()
	if existing := a.desktopViewer; existing != nil {
		a.desktopViewerMu.Unlock()
		cancel()
		_ = decoder.Close()
		_ = native.Close()
		existing.focusViewer()
		return map[string]any{"ok": true, "alreadyOpen": true}, nil
	}
	a.desktopViewer = session
	a.desktopViewerMu.Unlock()

	go session.run(ctx, a)
	return map[string]any{
		"ok":        true,
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
	if owner.bridge.RemoteDesktopAudioEnabled() {
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
			_ = viewer.Close()
		}
	}()
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

		status := owner.bridge.GetRemoteDesktopStatus()
		if status.State != "connected" || status.Backend != protocol.DesktopBackendRelay {
			return
		}

		cursorChanged := s.refreshCursor(owner)
		frame := owner.bridge.GetRemoteDesktopFrame()
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
					if requestErr := owner.bridge.RequestRemoteDesktopIDR(); requestErr != nil {
						log.Printf("[Desktop] native viewer generation IDR request failed: %v", requestErr)
					}
				}
				continue
			}
			if err := s.rebuildMediaPipeline(ctx, frame); err != nil {
				log.Printf("[Desktop] native viewer generation rebuild failed: %v", err)
				if time.Since(lastRecovery) >= 500*time.Millisecond {
					lastRecovery = time.Now()
					_ = owner.bridge.RequestRemoteDesktopIDR()
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
				if requestErr := owner.bridge.RequestRemoteDesktopIDR(); requestErr != nil {
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
		frame, config, err := owner.bridge.NextRemoteDesktopAudioFrame(ctx)
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
		decoder, hardware, gpuCursor := session.decoderStatus()
		return map[string]any{
			"open":      true,
			"hardware":  hardware,
			"decoder":   decoder,
			"gpuCursor": gpuCursor,
		}
	}
}
