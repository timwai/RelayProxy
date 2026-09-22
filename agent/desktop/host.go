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
	"sync"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type CaptureSource interface {
	Capture(context.Context) (*image.RGBA, error)
	Close() error
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

type HostConfig struct {
	MaxFPS      int
	MaxWidth    int
	MaxHeight   int
	JPEGQuality int
	MaxBitrate  int
	PacketSize  int
}

func DefaultHostConfig() HostConfig {
	return HostConfig{
		MaxFPS:      10,
		MaxWidth:    1280,
		MaxHeight:   720,
		JPEGQuality: 68,
		PacketSize:  1150,
	}
}

type Host struct {
	source CaptureSource
	input  InputSink
	cfg    HostConfig

	codecMu   sync.RWMutex
	codecCaps []protocol.DesktopCodecCapability

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
	return &Host{source: source, input: input, cfg: cfg}, nil
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
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	sessionConfig := ResolveHostConfig(h.cfg, options)
	backend := ""
	if source, ok := h.source.(SessionCaptureSource); ok {
		if err := source.BeginSession(ctx, sessionConfig); err != nil {
			return fmt.Errorf("start desktop capture session: %w", err)
		}
		defer source.EndSession()
		backend = source.CaptureBackend()
	}
	log.Printf("[Desktop] session capture=%s config=%dx%d fps=%d quality=%d maxBitrate=%d", backend, sessionConfig.MaxWidth, sessionConfig.MaxHeight, sessionConfig.MaxFPS, sessionConfig.JPEGQuality, sessionConfig.MaxBitrate)

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if h.input != nil {
		defer h.input.ReleaseAll()
	}
	idrRequests := make(chan struct{}, 1)
	bitrateUpdates := make(chan int, 1)

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
	errorsCh := make(chan error, workerCount)
	go func() {
		errorsCh <- h.streamSessionFrames(sessionCtx, conn, sessionConfig, options, idrRequests, bitrateUpdates)
	}()
	go func() {
		errorsCh <- h.readSessionControlLoop(
			sessionCtx, conn, idrRequests, bitrateUpdates, clipboardEndpoint, clipboardState, syncClipboard,
		)
	}()
	if hasCursor {
		go func() { errorsCh <- h.streamCursor(sessionCtx, conn, cursorSource) }()
	}
	if syncClipboard {
		go func() { errorsCh <- h.streamClipboard(sessionCtx, conn, clipboardEndpoint, clipboardState) }()
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

		case protocol.DesktopSessionVideoControl:
			if message.VideoControl == nil {
				continue
			}
			target := message.VideoControl.TargetBitrate
			if target < 250_000 || target > maxJPEGBitrate {
				log.Printf("[Desktop] ignoring invalid ABR target bitrate=%d", target)
				continue
			}
			select {
			case bitrateUpdates <- target:
			default:
				select {
				case <-bitrateUpdates:
				default:
				}
				select {
				case bitrateUpdates <- target:
				default:
				}
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

func (h *Host) streamFrames(ctx context.Context, conn *desktopmedia.MediaConn, cfg HostConfig) error {
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
	frameInterval := time.Second / time.Duration(cfg.MaxFPS)
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
			Generation: 1,
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
				CaptureFPS:    float64(sentFrames-lastReportFrames) / seconds,
				EncodeFPS:     float64(sentFrames-lastReportFrames) / seconds,
				ActualBitrate:     int64(float64((sentBytes-lastReportBytes)*8) / seconds),
				TargetBitrate:     int64(cfg.MaxBitrate),
				SendQueueDelayMs: sendQueueDelayMs,
				Path:              "relay",
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
		case <-ticker.C:
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
	h.codecCaps = append(h.codecCaps[:0], capabilities...)
	h.codecMu.Unlock()
}

func (h *Host) CodecCapabilities() []protocol.DesktopCodecCapability {
	if h == nil {
		return nil
	}
	h.codecMu.RLock()
	defer h.codecMu.RUnlock()
	return append([]protocol.DesktopCodecCapability(nil), h.codecCaps...)
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
