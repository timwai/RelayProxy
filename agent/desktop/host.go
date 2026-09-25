package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"

	desktopcodec "relayproxy/agent/desktop/codec"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type CaptureSource interface {
	Capture(context.Context) (*image.RGBA, error)
	Close() error
}

// RawCaptureSource is an optional zero-staging path for capture backends that
// already expose encoder-friendly pixels. Returned Pix may be borrowed from the
// capture backend and must be consumed before the next CaptureRaw, Capture or
// Close call. The bool is false when the backend cannot provide the requested
// frame without falling back to the regular RGBA path.
type RawCaptureSource interface {
	CaptureRaw(context.Context) (desktopcodec.RawFrame, bool, error)
}

type D3D11CaptureFrame struct {
	Device      uintptr
	Resource    uintptr
	Subresource uint32
	Width       int
	Height      int
	At          time.Time

	releaseOnce sync.Once
	release     func()
}

func (f *D3D11CaptureFrame) Valid() bool {
	return f != nil && f.Device != 0 && f.Resource != 0 &&
		f.Width > 0 && f.Height > 0
}

func (f *D3D11CaptureFrame) Close() {
	if f == nil {
		return
	}
	f.releaseOnce.Do(func() {
		if f.release != nil {
			f.release()
		}
	})
}

type D3D11CaptureSource interface {
	CaptureD3D11(context.Context) (*D3D11CaptureFrame, bool, error)
}

// CaptureFPSController is an optional capture-side rate control hook. The Host
// still owns its send ticker; backends such as WGC can additionally lower their
// producer cadence when ABR reduces the session FPS, avoiding frames that would
// otherwise be captured and discarded before encoding.
type CaptureFPSController interface {
	SetCaptureFPS(int) error
}

func applyCaptureFPS(source CaptureSource, fps int) {
	controller, ok := source.(CaptureFPSController)
	if !ok || fps <= 0 {
		return
	}
	if err := controller.SetCaptureFPS(fps); err != nil {
		log.Printf("[Desktop] capture backend fps update=%d failed: %v", fps, err)
	}
}

// SessionCaptureSource lets a backend acquire expensive per-session resources
// (for example IDXGIOutputDuplication) only while somebody is actually
// connected. CaptureSource remains deliberately small so the JPEG MVP and
// future hardware encoders can share the same Host lifecycle.
type SessionCaptureSource interface {
	BeginSession(context.Context, HostConfig) error
	EndSession() error
	CaptureBackend() string
}

func captureBackendName(source CaptureSource, fallback string) string {
	if sessionSource, ok := source.(SessionCaptureSource); ok {
		if backend := sessionSource.CaptureBackend(); backend != "" {
			return backend
		}
	}
	if fallback != "" {
		return fallback
	}
	return "generic"
}

// CaptureCapabilitySource exposes a fresh platform capture/display snapshot.
// Display IDs are intentionally session-local: callers should use them only
// while the corresponding authenticated Agent session remains online.
type CaptureCapabilitySource interface {
	DesktopCaptureCapabilities(context.Context) ([]protocol.DesktopCaptureCapability, []protocol.DesktopDisplayCapability, error)
}

// HostCapabilityProvider is implemented by the concrete Relay Desktop host so
// Agent authentication can advertise current media/display capabilities
// without coupling the Agent package to platform-specific capture code.
type HostCapabilityProvider interface {
	DesktopCapabilities(context.Context) protocol.DesktopCapabilities
}

// HostSessionFactory creates isolated capture/input state for one media
// association. System hosts use it so concurrent Relay Desktop viewers never
// share mutable DXGI/WGC streams or per-display input geometry.
type HostSessionFactory func() (CaptureSource, InputSink, error)

// SessionInputSink lets platform input map viewer-local normalized coordinates
// into the same display geometry that the capture session is streaming.
type SessionInputSink interface {
	BeginInputSession(context.Context, HostConfig) error
	EndInputSession() error
}

type HostConfig struct {
	MaxFPS         int
	MaxWidth       int
	MaxHeight      int
	JPEGQuality    int
	MaxBitrate     int
	PacketSize     int
	DisplayID      string
	Chroma         desktopcodec.ChromaFormat
	BitDepth       int
	CaptureBackend protocol.DesktopCaptureBackend
}

func DefaultHostConfig() HostConfig {
	return HostConfig{
		MaxFPS:      10,
		MaxWidth:    1280,
		MaxHeight:   720,
		JPEGQuality: 68,
		PacketSize:  1150,
		Chroma:      desktopcodec.Chroma420,
		BitDepth:    8,
	}
}

type Host struct {
	source         CaptureSource
	input          InputSink
	cfg            HostConfig
	audioOpen      audioCaptureFactory
	sessionFactory HostSessionFactory

	codecMu   sync.RWMutex
	codecCaps []protocol.DesktopCodecCapability
	gpuCap    *protocol.DesktopGPUCapability

	sessionMu sync.Mutex
	closeOnce sync.Once
}

func NewHost(source CaptureSource, cfg HostConfig) (*Host, error) {
	return NewHostWithInput(source, nil, cfg)
}

func NewHostWithInput(source CaptureSource, input InputSink, cfg HostConfig) (*Host, error) {
	if source == nil {
		return nil, errors.New("desktop capture source is required")
	}
	defaults := DefaultHostConfig()
	if cfg.MaxFPS <= 0 {
		cfg.MaxFPS = defaults.MaxFPS
	}
	if cfg.MaxFPS > 30 {
		cfg.MaxFPS = 30
	}
	if cfg.MaxWidth <= 0 {
		cfg.MaxWidth = defaults.MaxWidth
	}
	if cfg.MaxHeight <= 0 {
		cfg.MaxHeight = defaults.MaxHeight
	}
	if cfg.JPEGQuality <= 0 {
		cfg.JPEGQuality = defaults.JPEGQuality
	}
	if cfg.JPEGQuality < 25 {
		cfg.JPEGQuality = 25
	}
	if cfg.JPEGQuality > 95 {
		cfg.JPEGQuality = 95
	}
	if cfg.PacketSize <= desktopmedia.MediaHeaderSize {
		cfg.PacketSize = defaults.PacketSize
	}
	if cfg.Chroma == "" {
		cfg.Chroma = defaults.Chroma
	}
	if cfg.BitDepth == 0 {
		cfg.BitDepth = defaults.BitDepth
	}
	return &Host{source: source, input: input, cfg: cfg, audioOpen: openDefaultAudioCapture}, nil
}

func (h *Host) SetSessionFactory(factory HostSessionFactory) {
	if h == nil {
		return
	}
	h.sessionMu.Lock()
	h.sessionFactory = factory
	h.sessionMu.Unlock()
}

func (h *Host) isolatedSessionHost() (*Host, func(), bool, error) {
	if h == nil {
		return nil, nil, false, errors.New("Relay Desktop host is unavailable")
	}
	h.sessionMu.Lock()
	factory := h.sessionFactory
	h.sessionMu.Unlock()
	if factory == nil {
		return h, func() {}, false, nil
	}
	source, input, err := factory()
	if err != nil {
		return nil, nil, true, err
	}
	if source == nil {
		return nil, nil, true, errors.New("Relay Desktop session factory returned no capture source")
	}
	session := &Host{
		source:    source,
		input:     input,
		cfg:       h.cfg,
		audioOpen: h.audioOpen,
		codecCaps: h.CodecCapabilities(),
		gpuCap:    h.GPUCapability(),
	}
	cleanup := func() {
		_ = source.Close()
	}
	return session, cleanup, true, nil
}

const (
	maxJPEGWidth   = 3840
	maxJPEGHeight  = 2160
	maxJPEGFPS     = 30
	maxJPEGBitrate = 100_000_000
)

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

type desktopResolutionTarget struct {
	MaxWidth  int
	MaxHeight int
}

type desktopDisplaySwitchError struct {
	DisplayID  string
	Generation uint32
}

func (e *desktopDisplaySwitchError) Error() string {
	if e == nil {
		return "Relay Desktop display switch"
	}
	return fmt.Sprintf("Relay Desktop display switch to %q after generation %d", e.DisplayID, e.Generation)
}

func queueLatestResolution(ch chan desktopResolutionTarget, value desktopResolutionTarget) {
	if ch == nil {
		return
	}
	select {
	case ch <- value:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- value:
	default:
	}
}

func validateDesktopResolutionTarget(width, height, maxWidth, maxHeight int) (desktopResolutionTarget, error) {
	if width <= 0 || height <= 0 {
		return desktopResolutionTarget{}, errors.New("Relay Desktop resolution target requires width and height")
	}
	if width < 320 || height < 180 {
		return desktopResolutionTarget{}, errors.New("Relay Desktop resolution target is below the minimum 320x180 bounds")
	}
	if maxWidth > 0 && width > maxWidth {
		return desktopResolutionTarget{}, fmt.Errorf("Relay Desktop resolution width %d exceeds session maximum %d", width, maxWidth)
	}
	if maxHeight > 0 && height > maxHeight {
		return desktopResolutionTarget{}, fmt.Errorf("Relay Desktop resolution height %d exceeds session maximum %d", height, maxHeight)
	}
	return desktopResolutionTarget{MaxWidth: width, MaxHeight: height}, nil
}

func queueLatestString(ch chan string, value string) {
	if ch == nil {
		return
	}
	select {
	case ch <- value:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- value:
	default:
	}
}

func queueLatestInt(ch chan int, value int) {
	if ch == nil {
		return
	}
	select {
	case ch <- value:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- value:
	default:
	}
}

// ResolveHostConfig maps user-facing Relay Desktop preferences to the current
// JPEG MVP. These values are deliberately session-local so one controller
// cannot permanently alter Host defaults for another session.
func ResolveHostConfig(base HostConfig, options protocol.RemoteDesktopConnectOptions) HostConfig {
	cfg := base
	switch options.Quality {
	case protocol.DesktopQualitySmooth:
		cfg.MaxWidth, cfg.MaxHeight, cfg.MaxFPS, cfg.JPEGQuality, cfg.MaxBitrate = 960, 540, 15, 55, 3_000_000
	case protocol.DesktopQualityBalanced:
		cfg.MaxWidth, cfg.MaxHeight, cfg.MaxFPS, cfg.JPEGQuality, cfg.MaxBitrate = 1280, 720, 20, 68, 6_000_000
	case protocol.DesktopQualityHigh:
		cfg.MaxWidth, cfg.MaxHeight, cfg.MaxFPS, cfg.JPEGQuality, cfg.MaxBitrate = 1920, 1080, 30, 78, 12_000_000
	case protocol.DesktopQualityExtreme:
		cfg.MaxWidth, cfg.MaxHeight, cfg.MaxFPS, cfg.JPEGQuality, cfg.MaxBitrate = 2560, 1440, 30, 85, 20_000_000
	}

	resolution := options.Resolution
	switch resolution.Mode {
	case "native":
		cfg.MaxWidth, cfg.MaxHeight = maxJPEGWidth, maxJPEGHeight
	case "fixed":
		if resolution.Width > 0 {
			cfg.MaxWidth = resolution.Width
		}
		if resolution.Height > 0 {
			cfg.MaxHeight = resolution.Height
		}
	}
	if resolution.MaxWidth > 0 {
		cfg.MaxWidth = resolution.MaxWidth
	}
	if resolution.MaxHeight > 0 {
		cfg.MaxHeight = resolution.MaxHeight
	}
	if options.FPS > 0 {
		cfg.MaxFPS = options.FPS
	}
	if options.MaxBitrate > 0 {
		cfg.MaxBitrate = options.MaxBitrate
	}
	cfg.DisplayID = options.DisplayID
	switch options.Chroma {
	case protocol.DesktopChroma444:
		cfg.Chroma = desktopcodec.Chroma444
	default:
		cfg.Chroma = desktopcodec.Chroma420
	}
	cfg.BitDepth = 8
	cfg.CaptureBackend = options.CaptureBackend
	if cfg.CaptureBackend == "" {
		cfg.CaptureBackend = protocol.DesktopCaptureAuto
	}

	cfg.MaxWidth = clampInt(cfg.MaxWidth, 320, maxJPEGWidth)
	cfg.MaxHeight = clampInt(cfg.MaxHeight, 180, maxJPEGHeight)
	cfg.MaxFPS = clampInt(cfg.MaxFPS, 1, maxJPEGFPS)
	cfg.JPEGQuality = clampInt(cfg.JPEGQuality, 25, 95)
	if cfg.MaxBitrate > 0 {
		cfg.MaxBitrate = clampInt(cfg.MaxBitrate, 250_000, maxJPEGBitrate)
	}
	return cfg
}

func newMediaSessionID() (uint64, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint64(raw[:])
	if id == 0 {
		id = 1
	}
	return id, nil
}

// HandleDesktopMedia owns one host-side media session. The MVP intentionally
// uses independent JPEG frames; later DXGI + H.264 replaces capture/encoding
// behind this method without changing the RD/1 transport or session API.
func (h *Host) HandleDesktopMedia(ctx context.Context, conn *desktopmedia.MediaConn, options protocol.RemoteDesktopConnectOptions) error {
	if h == nil || conn == nil {
		return errors.New("desktop media connection is unavailable")
	}
	sessionHost, cleanup, isolated, err := h.isolatedSessionHost()
	if err != nil {
		return fmt.Errorf("create Relay Desktop session resources: %w", err)
	}
	if isolated {
		defer cleanup()
		return sessionHost.handleDesktopMedia(ctx, conn, options)
	}

	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	return h.handleDesktopMedia(ctx, conn, options)
}

func (h *Host) handleDesktopMedia(ctx context.Context, conn *desktopmedia.MediaConn, options protocol.RemoteDesktopConnectOptions) error {
	sessionConfig := ResolveHostConfig(h.cfg, options)
	backend := "generic"
	if source, ok := h.source.(SessionCaptureSource); ok {
		if err := source.BeginSession(ctx, sessionConfig); err != nil {
			return fmt.Errorf("start desktop capture session: %w", err)
		}
		defer source.EndSession()
		backend = source.CaptureBackend()
		if backend == "" {
			backend = "generic"
		}
	}
	if input, ok := h.input.(SessionInputSink); ok {
		if err := input.BeginInputSession(ctx, sessionConfig); err != nil {
			return fmt.Errorf("start desktop input session: %w", err)
		}
		defer input.EndInputSession()
	}
	log.Printf("[Desktop] session capture=%s requestedCapture=%s display=%q config=%dx%d fps=%d quality=%d maxBitrate=%d chroma=%s bitDepth=%d", backend, sessionConfig.CaptureBackend, sessionConfig.DisplayID, sessionConfig.MaxWidth, sessionConfig.MaxHeight, sessionConfig.MaxFPS, sessionConfig.JPEGQuality, sessionConfig.MaxBitrate, sessionConfig.Chroma, sessionConfig.BitDepth)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if h.input != nil {
		defer h.input.ReleaseAll()
	}
	idrRequests := make(chan struct{}, 1)
	bitrateUpdates := make(chan int, 1)
	fpsUpdates := make(chan int, 1)
	resolutionUpdates := make(chan desktopResolutionTarget, 1)
	displayUpdates := make(chan string, 1)
	audioLossUpdates := make(chan int, 1)

	workerCount := 2
	cursorSource, hasCursor := h.source.(CursorCaptureSource)
	if hasCursor {
		workerCount++
	}
	clipboardEndpoint, hasClipboard := h.source.(ClipboardEndpoint)
	syncClipboard := hasClipboard && clipboardEnabled(options)
	clipboardState := &clipboardSyncState{}
	if syncClipboard {
		workerCount++
	}
	streamAudio := h.desktopAudioAvailable() && desktopAudioEnabled(options)
	if streamAudio {
		workerCount++
	}
	displayProvider, streamDisplays := h.source.(CaptureCapabilitySource)
	if streamDisplays {
		workerCount++
	}
	errorsCh := make(chan error, workerCount)
	go func() {
		errorsCh <- h.streamSessionFrames(
			sessionCtx, conn, sessionConfig, options, backend,
			idrRequests, bitrateUpdates, fpsUpdates, resolutionUpdates, displayUpdates,
		)
	}()
	go func() {
		errorsCh <- h.readSessionControlLoop(
			sessionCtx, conn, idrRequests, bitrateUpdates, fpsUpdates, resolutionUpdates, displayUpdates, audioLossUpdates,
			sessionConfig.MaxFPS, sessionConfig.MaxWidth, sessionConfig.MaxHeight,
			clipboardEndpoint, clipboardState, syncClipboard,
		)
	}()
	if hasCursor {
		go func() { errorsCh <- h.streamCursor(sessionCtx, conn, cursorSource) }()
	}
	if syncClipboard {
		go func() { errorsCh <- h.streamClipboard(sessionCtx, conn, clipboardEndpoint, clipboardState) }()
	}
	if streamAudio {
		go func() {
			err := h.streamSessionAudio(sessionCtx, conn, options, audioLossUpdates)
			if err != nil && !errors.Is(err, context.Canceled) && sessionCtx.Err() == nil {
				log.Printf("[Desktop] audio capture disabled for this session: %v", err)
			}
			if sessionCtx.Err() == nil {
				<-sessionCtx.Done()
			}
			errorsCh <- sessionCtx.Err()
		}()
	}
	if streamDisplays {
		go func() {
			errorsCh <- h.streamSessionDisplays(sessionCtx, conn, displayProvider)
		}()
	}

	first := <-errorsCh
	cancel()
	_ = conn.Close()
	var sessionErr error
	if first != nil && !errors.Is(first, context.Canceled) {
		sessionErr = first
	}
	for i := 1; i < workerCount; i++ {
		err := <-errorsCh
		if sessionErr == nil && err != nil && !errors.Is(err, context.Canceled) {
			sessionErr = err
		}
	}
	if sessionErr != nil {
		return sessionErr
	}
	return ctx.Err()
}

func (h *Host) readSessionControlLoop(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	idrRequests chan<- struct{},
	bitrateUpdates chan int,
	fpsUpdates chan int,
	resolutionUpdates chan desktopResolutionTarget,
	displayUpdates chan string,
	audioLossUpdates chan int,
	maxFPS int,
	maxWidth int,
	maxHeight int,
	clipboard ClipboardEndpoint,
	clipboardState *clipboardSyncState,
	syncClipboard bool,
) error {
	var lastInputSequence uint64
	var lastClipboardSequence uint64
	for {
		message, err := conn.ReceiveSessionMessage(ctx)
		if err != nil {
			return err
		}
		switch message.Type {
		case protocol.DesktopSessionIDRRequest:
			select {
			case idrRequests <- struct{}{}:
			default:
			}
			continue

		case protocol.DesktopSessionPing:
			if message.Probe == nil {
				continue
			}
			probe := *message.Probe
			if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
				Type:  protocol.DesktopSessionPong,
				Probe: &probe,
			}); err != nil {
				return err
			}
			continue

		case protocol.DesktopSessionAudioControl:
			if message.AudioControl == nil {
				continue
			}
			loss := message.AudioControl.ExpectedLossPercent
			if loss < 0 || loss > 100 {
				log.Printf("[Desktop] ignoring invalid Opus expected loss=%d", loss)
				continue
			}
			queueLatestInt(audioLossUpdates, loss)
			continue

		case protocol.DesktopSessionVideoControl:
			if message.VideoControl == nil {
				continue
			}
			control := message.VideoControl
			if control.TargetBitrate != 0 {
				if control.TargetBitrate < 250_000 || control.TargetBitrate > maxJPEGBitrate {
					log.Printf("[Desktop] ignoring invalid ABR target bitrate=%d", control.TargetBitrate)
				} else {
					queueLatestInt(bitrateUpdates, control.TargetBitrate)
				}
			}
			if control.TargetFPS != 0 {
				if maxFPS <= 0 || control.TargetFPS < 1 || control.TargetFPS > maxFPS {
					log.Printf("[Desktop] ignoring invalid ABR target fps=%d max=%d", control.TargetFPS, maxFPS)
				} else {
					queueLatestInt(fpsUpdates, control.TargetFPS)
				}
			}
			if control.TargetWidth != 0 || control.TargetHeight != 0 {
				target, err := validateDesktopResolutionTarget(
					control.TargetWidth, control.TargetHeight, maxWidth, maxHeight,
				)
				if err != nil {
					log.Printf("[Desktop] ignoring invalid resolution control %dx%d: %v",
						control.TargetWidth, control.TargetHeight, err)
				} else {
					queueLatestResolution(resolutionUpdates, target)
				}
			}
			if control.DisplayID != nil {
				queueLatestString(displayUpdates, strings.TrimSpace(*control.DisplayID))
			}
			continue

		case protocol.DesktopSessionInput:
			if message.Input == nil {
				return errors.New("Relay Desktop input message is missing the event")
			}
			if h.input == nil {
				continue
			}
			event := *message.Input
			if err := ValidateDesktopInputEvent(event); err != nil {
				return err
			}
			if event.Sequence != 0 {
				if event.Sequence <= lastInputSequence {
					continue
				}
				lastInputSequence = event.Sequence
			}
			if err := h.input.ApplyInput(ctx, event); err != nil {
				// Input injection can be rejected by Windows UIPI when the remote
				// foreground process is more privileged than the Agent. Keep video
				// alive and surface the failure through logs instead of tearing down.
				log.Printf("[Desktop] input injection failed: %v", err)
			}

		case protocol.DesktopSessionClipboard:
			if message.Clipboard == nil || !syncClipboard || clipboard == nil || clipboardState == nil {
				continue
			}
			update := *message.Clipboard
			if update.Sequence != 0 {
				if update.Sequence <= lastClipboardSequence {
					continue
				}
				lastClipboardSequence = update.Sequence
			}
			text, err := validateClipboardText(update.Text)
			if err != nil {
				log.Printf("[Desktop] remote clipboard ignored: %v", err)
				continue
			}
			if clipboardState.IsCurrent(text) {
				continue
			}
			if err := clipboard.SetClipboardText(ctx, text); err != nil {
				log.Printf("[Desktop] apply remote clipboard failed: %v", err)
				continue
			}
			clipboardState.Seed(text)

		default:
			return errors.New("invalid Relay Desktop session control message")
		}
	}
}

func (h *Host) streamFrames(
	ctx context.Context,
	conn *desktopmedia.MediaConn,
	cfg HostConfig,
	captureBackend string,
	generation uint32,
	fpsUpdates <-chan int,
	displayUpdates <-chan string,
) error {
	sessionID, err := newMediaSessionID()
	if err != nil {
		return err
	}
	var frameID uint32 = 1
	var sequence uint32 = 1
	var sentFrames uint64
	var sentBytes uint64
	lastReportAt := time.Now()
	var lastReportFrames uint64
	var lastReportBytes uint64
	var sendQueueDelayMs float64
	var droppedFrames uint64
	targetFPS := cfg.MaxFPS
	frameInterval := frameIntervalForFPS(targetFPS)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	sendFrame := func() error {
		encoded, err := h.captureJPEGWithConfig(ctx, cfg)
		if err != nil {
			return err
		}
		frame := desktopmedia.EncodedFrame{
			SessionID:  sessionID,
			StreamID:   1,
			Generation: generation,
			FrameID:    frameID,
			Timestamp:  uint64(time.Now().UnixMicro()),
			KeyFrame:   true,
			Data:       encoded,
		}
		packets, next, err := desktopmedia.PacketizeFrame(frame, cfg.PacketSize, sequence)
		if err != nil {
			return err
		}
		for _, packet := range packets {
			started := time.Now()
			err := conn.Send(ctx, packet)
			sendQueueDelayMs = smoothSendQueueDelayMs(sendQueueDelayMs, time.Since(started))
			if err != nil {
				return err
			}
		}
		frameID++
		sequence = next
		sentFrames++
		sentBytes += uint64(len(encoded))
		now := time.Now()
		if elapsed := now.Sub(lastReportAt); elapsed >= time.Second {
			seconds := elapsed.Seconds()
			stats := protocol.DesktopSessionStats{
				CaptureFPS:       float64(sentFrames-lastReportFrames) / seconds,
				EncodeFPS:        float64(sentFrames-lastReportFrames) / seconds,
				ActualBitrate:    int64(float64((sentBytes-lastReportBytes)*8) / seconds),
				TargetBitrate:    int64(cfg.MaxBitrate),
				TargetFPS:        targetFPS,
				SendQueueDelayMs: sendQueueDelayMs,
				DroppedFrames:    droppedFrames,
				Path:             "relay",
				CaptureBackend:   captureBackendName(h.source, captureBackend),
				CaptureFormat:    "rgba",
				EncoderBackend:   "jpeg-go",
			}
			if err := conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
				Type:  protocol.DesktopSessionStatsReport,
				Stats: &stats,
			}); err != nil {
				return err
			}
			lastReportAt = now
			lastReportFrames = sentFrames
			lastReportBytes = sentBytes
		}
		return nil
	}

	if err := sendFrame(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case displayID := <-displayUpdates:
			if displayID == cfg.DisplayID {
				continue
			}
			return &desktopDisplaySwitchError{DisplayID: displayID, Generation: generation}
		case nextFPS := <-fpsUpdates:
			nextFPS = clampInt(nextFPS, 1, cfg.MaxFPS)
			if nextFPS == targetFPS {
				continue
			}
			targetFPS = nextFPS
			frameInterval = frameIntervalForFPS(targetFPS)
			ticker.Reset(frameInterval)
			applyCaptureFPS(h.source, targetFPS)
			log.Printf("[Desktop] JPEG capture fps updated=%d", targetFPS)
		case scheduled := <-ticker.C:
			if dropped := staleScheduledFrameCount(scheduled, time.Now(), frameInterval); dropped > 0 {
				droppedFrames += dropped
				continue
			}
			if err := sendFrame(); err != nil {
				return err
			}
		}
	}
}

func (h *Host) captureJPEG(ctx context.Context) ([]byte, error) {
	return h.captureJPEGWithConfig(ctx, h.cfg)
}

func (h *Host) captureJPEGWithConfig(ctx context.Context, cfg HostConfig) ([]byte, error) {
	frame, err := h.source.Capture(ctx)
	if err != nil {
		return nil, err
	}
	frame = fitRGBA(frame, cfg.MaxWidth, cfg.MaxHeight)
	quality := cfg.JPEGQuality
	if quality <= 0 {
		quality = DefaultHostConfig().JPEGQuality
	}
	var frameBudget int
	if cfg.MaxBitrate > 0 && cfg.MaxFPS > 0 {
		frameBudget = cfg.MaxBitrate / 8 / cfg.MaxFPS
	}

	var encoded []byte
	for attempt := 0; attempt < 4; attempt++ {
		var out bytes.Buffer
		if err := jpeg.Encode(&out, frame, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		encoded = append(encoded[:0], out.Bytes()...)
		// JPEG has no true rate controller. Treat MaxBitrate as a soft per-frame
		// budget until H.264 replaces this MVP encoder.
		if frameBudget <= 0 || len(encoded) <= frameBudget*11/10 || quality <= 25 {
			break
		}
		quality = clampInt(quality-10, 25, 95)
	}
	return encoded, nil
}

func fitRGBA(src *image.RGBA, maxWidth, maxHeight int) *image.RGBA {
	if src == nil {
		return nil
	}
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw <= 0 || sh <= 0 || maxWidth <= 0 || maxHeight <= 0 || (sw <= maxWidth && sh <= maxHeight) {
		return src
	}
	dw, dh := maxWidth, sh*maxWidth/sw
	if dh > maxHeight {
		dh = maxHeight
		dw = sw * maxHeight / sh
	}
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := bounds.Min.Y + y*sh/dh
		for x := 0; x < dw; x++ {
			sx := bounds.Min.X + x*sw/dw
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(x, y)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
	return dst
}

func (h *Host) SetCodecCapabilities(capabilities []protocol.DesktopCodecCapability) {
	if h == nil {
		return
	}
	h.codecMu.Lock()
	h.codecCaps = protocol.CloneDesktopCodecCapabilities(capabilities)
	h.codecMu.Unlock()
}

func (h *Host) CodecCapabilities() []protocol.DesktopCodecCapability {
	if h == nil {
		return nil
	}
	h.codecMu.RLock()
	defer h.codecMu.RUnlock()
	return protocol.CloneDesktopCodecCapabilities(h.codecCaps)
}

func (h *Host) SetGPUCapability(capability *protocol.DesktopGPUCapability) {
	if h == nil {
		return
	}
	h.codecMu.Lock()
	h.gpuCap = protocol.CloneDesktopGPUCapability(capability)
	h.codecMu.Unlock()
}

func (h *Host) GPUCapability() *protocol.DesktopGPUCapability {
	if h == nil {
		return nil
	}
	h.codecMu.RLock()
	defer h.codecMu.RUnlock()
	return protocol.CloneDesktopGPUCapability(h.gpuCap)
}

func (h *Host) DesktopCapabilities(ctx context.Context) protocol.DesktopCapabilities {
	if h == nil {
		return protocol.DesktopCapabilities{}
	}
	h.sessionMu.Lock()
	multiStream := h.sessionFactory != nil
	h.sessionMu.Unlock()
	caps := protocol.DesktopCapabilities{
		RelayDesktop: true,
		MultiStream:  multiStream,
		Codecs:       h.CodecCapabilities(),
		GPU:          h.GPUCapability(),
		MaxWidth:     maxJPEGWidth,
		MaxHeight:    maxJPEGHeight,
		MaxFPS:       maxJPEGFPS,
	}
	if _, ok := h.source.(ClipboardEndpoint); ok {
		caps.Clipboard = true
	}
	if h.desktopAudioAvailable() {
		caps.Audio = true
		caps.AudioCodecs = []string{protocol.DesktopAudioCodecOpus, protocol.DesktopAudioCodecPCMS16LE}
	}
	_, hasCursor := h.source.(CursorCaptureSource)
	if provider, ok := h.source.(CaptureCapabilitySource); ok {
		captures, displays, err := provider.DesktopCaptureCapabilities(ctx)
		if err == nil {
			caps.Captures = append([]protocol.DesktopCaptureCapability(nil), captures...)
			caps.Displays = append([]protocol.DesktopDisplayCapability(nil), displays...)
			caps.MultiMonitor = len(displays) > 1
		}
	}
	if len(caps.Captures) == 0 {
		caps.Captures = []protocol.DesktopCaptureCapability{{
			Backend: "generic",
			Cursor:  hasCursor,
		}}
	}
	return caps
}

func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	var err error
	h.closeOnce.Do(func() {
		if h.source != nil {
			err = h.source.Close()
		}
	})
	return err
}
