package desktop

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"

	desktopadapt "relayproxy/agent/desktop/adapt"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type DesktopMediaDialer func(context.Context, string) (*desktopmedia.MediaConn, error)

type FrameSnapshot struct {
	Sequence   uint64
	Generation uint32
	MimeType   string
	Codec      string
	Width      int
	Height     int
	Timestamp  uint64
	KeyFrame   bool
	Data       []byte
}

type ControllerSession struct {
	targetID string
	conn     *desktopmedia.MediaConn
	cancel   context.CancelFunc
	done     chan struct{}

	closeOnce        sync.Once
	mu               sync.RWMutex
	latest           FrameSnapshot
	latestCursor     protocol.DesktopCursorState
	latestClipboard  protocol.DesktopClipboardState
	clipboardSendSeq uint64
	videoConfig      protocol.DesktopVideoConfig
	followViewport   bool
	configReady      chan struct{}
	configOnce       sync.Once

	audioMu             sync.Mutex
	audioConfig         protocol.DesktopAudioConfig
	audioQueue          []AudioFrameSnapshot
	audioNotify         chan struct{}
	audioStats          audioRuntimeCounters
	audioConcealmentRun int

	recoveryMu sync.Mutex
	recovery   h264RecoveryState

	stats       *sessionStatsTracker
	diagnostics *sessionDiagnosticsRecorder

	options protocol.RemoteDesktopConnectOptions
	abrMu   sync.Mutex
	abr     *desktopadapt.Controller
}

func StartController(parent context.Context, targetID string, dial DesktopMediaDialer) (*ControllerSession, error) {
	return StartControllerWithOptions(parent, targetID, dial, protocol.RemoteDesktopConnectOptions{})
}

func StartControllerWithOptions(
	parent context.Context,
	targetID string,
	dial DesktopMediaDialer,
	options protocol.RemoteDesktopConnectOptions,
) (*ControllerSession, error) {
	if targetID == "" {
		return nil, errors.New("Relay Desktop target id is required")
	}
	if dial == nil {
		return nil, errors.New("Relay Desktop media dialer is required")
	}
	conn, err := dial(parent, targetID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	now := time.Now()
	session := &ControllerSession{
		targetID:       targetID,
		conn:           conn,
		cancel:         cancel,
		done:           make(chan struct{}),
		configReady:    make(chan struct{}),
		audioNotify:    make(chan struct{}, 1),
		stats:          newSessionStatsTracker("relay"),
		diagnostics:    newSessionDiagnosticsRecorder(targetID, options, now),
		options:        options,
		followViewport: desktopResolutionModeFollowsViewport(options.Resolution.Mode),
	}
	go session.controlLoop(ctx)
	go session.probeLoop(ctx)
	go session.abrLoop(ctx)
	go session.audioControlLoop(ctx)
	go session.readLoop(ctx)
	return session, nil
}

func (s *ControllerSession) controlLoop(ctx context.Context) {
	for {
		message, err := s.conn.ReceiveSessionMessage(ctx)
		if err != nil {
			return
		}
		switch message.Type {
		case protocol.DesktopSessionVideoConfig:
			if message.VideoConfig == nil {
				continue
			}
			config := *message.VideoConfig
			previousConfig := s.VideoConfigSnapshot()
			if !s.applyVideoConfig(config) {
				continue
			}
			if videoConfigRequiresABRReset(previousConfig, config) {
				s.configureABR(config)
			} else {
				s.syncABRResolution(config)
			}
			s.configOnce.Do(func() { close(s.configReady) })

		case protocol.DesktopSessionAudioConfig:
			if message.AudioConfig == nil {
				continue
			}
			if !s.applyAudioConfig(*message.AudioConfig) {
				log.Printf("[Desktop] invalid or stale audio config ignored: %+v", *message.AudioConfig)
			}

		case protocol.DesktopSessionCursor:
			if message.Cursor == nil {
				continue
			}
			cursor := *message.Cursor
			s.mu.Lock()
			if len(cursor.PNG) == 0 && cursor.CursorID != "" && cursor.CursorID == s.latestCursor.CursorID {
				cursor.PNG = append([]byte(nil), s.latestCursor.PNG...)
			} else {
				cursor.PNG = append([]byte(nil), cursor.PNG...)
			}
			s.latestCursor = cursor
			s.mu.Unlock()

		case protocol.DesktopSessionClipboard:
			if message.Clipboard == nil {
				continue
			}
			clipboard := *message.Clipboard
			text, err := validateClipboardText(clipboard.Text)
			if err != nil {
				log.Printf("[Desktop] remote clipboard update ignored: %v", err)
				continue
			}
			clipboard.Text = text
			s.mu.Lock()
			if clipboard.Sequence > s.latestClipboard.Sequence {
				s.latestClipboard = clipboard
			}
			s.mu.Unlock()

		case protocol.DesktopSessionPong:
			if message.Probe != nil && s.stats != nil {
				s.stats.ObservePong(*message.Probe, time.Now())
			}

		case protocol.DesktopSessionStatsReport:
			if message.Stats != nil && s.stats != nil {
				s.stats.MergeRemote(*message.Stats)
			}
		}
	}
}

func videoConfigRequiresABRReset(previous, next protocol.DesktopVideoConfig) bool {
	if previous.Codec == "" {
		return true
	}
	if previous.Codec != next.Codec || previous.FPS != next.FPS {
		return true
	}
	previousMaxBitrate := previous.MaxBitrate
	if previousMaxBitrate <= 0 {
		previousMaxBitrate = previous.TargetBitrate
	}
	nextMaxBitrate := next.MaxBitrate
	if nextMaxBitrate <= 0 {
		nextMaxBitrate = next.TargetBitrate
	}
	return previousMaxBitrate != nextMaxBitrate
}

func videoConfigResolutionCeiling(config protocol.DesktopVideoConfig) (int, int) {
	maxWidth := config.MaxWidth
	if maxWidth <= 0 {
		maxWidth = config.Width
	}
	maxHeight := config.MaxHeight
	if maxHeight <= 0 {
		maxHeight = config.Height
	}
	return maxWidth, maxHeight
}

func videoConfigResolutionScale(config protocol.DesktopVideoConfig) int {
	maxWidth, maxHeight := videoConfigResolutionCeiling(config)
	if config.Width <= 0 || config.Height <= 0 || maxWidth <= 0 || maxHeight <= 0 {
		return 100
	}
	widthScale := (config.Width*100 + maxWidth - 1) / maxWidth
	heightScale := (config.Height*100 + maxHeight - 1) / maxHeight
	scale := widthScale
	if heightScale > scale {
		scale = heightScale
	}
	if scale < 1 {
		scale = 1
	}
	if scale > 100 {
		scale = 100
	}
	return scale
}

func resolutionBoundsForScale(config protocol.DesktopVideoConfig, scale int) (int, int, bool) {
	maxWidth, maxHeight := videoConfigResolutionCeiling(config)
	if maxWidth <= 0 || maxHeight <= 0 || scale <= 0 {
		return 0, 0, false
	}
	if scale > 100 {
		scale = 100
	}
	width := maxWidth * scale / 100
	height := maxHeight * scale / 100
	if width < 320 {
		width = 320
	}
	if height < 180 {
		height = 180
	}
	if width > maxWidth {
		width = maxWidth
	}
	if height > maxHeight {
		height = maxHeight
	}
	width &^= 1
	height &^= 1
	if width < 320 || height < 180 {
		return 0, 0, false
	}
	return width, height, true
}

func (s *ControllerSession) syncABRResolution(config protocol.DesktopVideoConfig) {
	s.abrMu.Lock()
	defer s.abrMu.Unlock()
	if s.abr == nil {
		return
	}
	s.abr.SetResolutionScale(videoConfigResolutionScale(config))
}

func (s *ControllerSession) configureABR(config protocol.DesktopVideoConfig) {
	s.abrMu.Lock()
	defer s.abrMu.Unlock()
	if !desktopAdaptiveVideoCodec(config.Codec) || config.TargetBitrate <= 0 {
		s.abr = nil
		return
	}
	maxBitrate := config.MaxBitrate
	if maxBitrate <= 0 {
		maxBitrate = config.TargetBitrate
	}
	cfg := desktopadapt.DefaultConfig(s.options.Scene, maxBitrate)
	cfg.InitialBitrate = config.TargetBitrate
	maxWidth, maxHeight := videoConfigResolutionCeiling(config)
	cfg.MinResolutionScale = desktopadapt.AdaptiveMinResolutionScale(s.options.Scene, maxWidth, maxHeight)
	cfg.InitialResolutionScale = videoConfigResolutionScale(config)
	if config.FPS > 0 {
		cfg.MaxFPS = config.FPS
		cfg.InitialFPS = config.FPS
		cfg.MinFPS = desktopadapt.AdaptiveMinFPS(s.options.Scene, config.FPS)
	}
	s.abr = desktopadapt.NewController(cfg)
}

func (s *ControllerSession) abrDecision(stats protocol.DesktopSessionStats) desktopadapt.MediaDecision {
	s.abrMu.Lock()
	defer s.abrMu.Unlock()
	if s.abr == nil {
		return desktopadapt.MediaDecision{}
	}
	return s.abr.Observe(stats)
}

func abrVideoControl(
	config protocol.DesktopVideoConfig,
	decision desktopadapt.MediaDecision,
) protocol.DesktopVideoControl {
	control := protocol.DesktopVideoControl{
		TargetBitrate: decision.TargetBitrate,
		TargetFPS:     decision.TargetFPS,
	}
	if decision.ResolutionChanged {
		if width, height, ok := resolutionBoundsForScale(config, decision.TargetResolutionScale); ok {
			control.TargetWidth = width
			control.TargetHeight = height
		}
	}
	return control
}

func (s *ControllerSession) abrLoop(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.stats == nil {
				continue
			}
			now := time.Now()
			decision := s.abrDecision(s.stats.AdaptationSnapshot(now))
			config := s.VideoConfigSnapshot()
			if s.diagnostics != nil {
				s.diagnostics.Record(
					now,
					config,
					s.stats.DiagnosticsSnapshot(now),
					s.AudioDiagnosticsSnapshot(),
					decision,
				)
			}
			if !decision.Changed {
				continue
			}
			control := abrVideoControl(config, decision)
			if control.TargetBitrate <= 0 && control.TargetFPS <= 0 &&
				(control.TargetWidth <= 0 || control.TargetHeight <= 0) {
				continue
			}
			controlCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := s.conn.SendSessionMessage(controlCtx, protocol.DesktopSessionMessage{
				Type:         protocol.DesktopSessionVideoControl,
				VideoControl: &control,
			})
			cancel()
			if err != nil {
				log.Printf("[Desktop] ABR media control failed: %v", err)
				return
			}
			log.Printf("[Desktop] ABR target bitrate=%d fps=%d resolution=%dx%d scale=%d%% reason=%s",
				decision.TargetBitrate, decision.TargetFPS, control.TargetWidth, control.TargetHeight,
				decision.TargetResolutionScale, decision.Reason)
		}
	}
}

func (s *ControllerSession) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if s.stats == nil {
				continue
			}
			probe := s.stats.NewProbe(now)
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := s.conn.SendSessionMessage(probeCtx, protocol.DesktopSessionMessage{
				Type:  protocol.DesktopSessionPing,
				Probe: &probe,
			})
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *ControllerSession) applyVideoConfig(config protocol.DesktopVideoConfig) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	current := s.videoConfig
	if current.Generation != 0 && (config.Generation == 0 || config.Generation < current.Generation) {
		s.mu.Unlock()
		return false
	}
	generationChanged := current.Generation != 0 && config.Generation != 0 && current.Generation != config.Generation
	s.videoConfig = config
	if generationChanged {
		s.latest = FrameSnapshot{}
	}
	s.mu.Unlock()
	if generationChanged {
		s.recoveryMu.Lock()
		s.recovery.Reset()
		s.recoveryMu.Unlock()
	}
	return true
}

func frameMatchesVideoConfig(frame *desktopmedia.EncodedFrame, config protocol.DesktopVideoConfig, configured bool) bool {
	if frame == nil {
		return false
	}
	if !configured || config.Generation == 0 {
		return true
	}
	return frame.Generation == config.Generation
}

func (s *ControllerSession) VideoConfigSnapshot() protocol.DesktopVideoConfig {
	if s == nil {
		return protocol.DesktopVideoConfig{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.videoConfig
}

func (s *ControllerSession) currentVideoConfig(ctx context.Context) (protocol.DesktopVideoConfig, bool) {
	s.mu.RLock()
	config := s.videoConfig
	s.mu.RUnlock()
	if config.Codec != "" {
		return config, true
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.DesktopVideoConfig{}, false
	case <-s.configReady:
		s.mu.RLock()
		config = s.videoConfig
		s.mu.RUnlock()
		return config, config.Codec != ""
	case <-timer.C:
		return protocol.DesktopVideoConfig{}, false
	}
}

func desktopVideoMIME(codec string) string {
	switch codec {
	case "h264":
		return "video/h264"
	case "h265":
		return "video/h265"
	default:
		return ""
	}
}

func desktopAdaptiveVideoCodec(codec string) bool {
	return codec == "h264" || codec == "h265"
}

func snapshotFromEncodedFrame(frame *desktopmedia.EncodedFrame, config protocol.DesktopVideoConfig, configured bool) (FrameSnapshot, bool) {
	if frame == nil {
		return FrameSnapshot{}, false
	}
	snapshot := FrameSnapshot{
		Sequence:   uint64(frame.FrameID),
		Generation: frame.Generation,
		Timestamp:  frame.Timestamp,
		KeyFrame:   frame.KeyFrame,
		Data:       append([]byte(nil), frame.Data...),
	}
	if configured {
		if mimeType := desktopVideoMIME(config.Codec); mimeType != "" {
			snapshot.MimeType = mimeType
			snapshot.Codec = config.CodecString
			snapshot.Width = config.Width
			snapshot.Height = config.Height
			return snapshot, true
		}
		if config.Codec != "" && config.Codec != "jpeg" {
			return FrameSnapshot{}, false
		}
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame.Data))
	if err != nil {
		return FrameSnapshot{}, false
	}
	snapshot.MimeType = "image/jpeg"
	snapshot.Codec = "jpeg"
	snapshot.Width = cfg.Width
	snapshot.Height = cfg.Height
	return snapshot, true
}

func (s *ControllerSession) sendIDRRequest(ctx context.Context) error {
	return s.conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type: protocol.DesktopSessionIDRRequest,
	})
}

func (s *ControllerSession) markIDRRequestFailed() {
	s.recoveryMu.Lock()
	s.recovery.RequestFailed()
	s.recoveryMu.Unlock()
}

func desktopResolutionModeFollowsViewport(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto", "follow_viewport":
		return true
	default:
		return false
	}
}

func (s *ControllerSession) ViewportFollowEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.followViewport
}

func (s *ControllerSession) requestResolution(ctx context.Context, width, height int, followViewport bool) error {
	if s == nil || !s.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	config := s.VideoConfigSnapshot()
	if !desktopAdaptiveVideoCodec(config.Codec) {
		return errors.New("runtime resolution switching requires H.264 or H.265")
	}
	maxWidth := config.MaxWidth
	if maxWidth <= 0 {
		maxWidth = maxJPEGWidth
	}
	maxHeight := config.MaxHeight
	if maxHeight <= 0 {
		maxHeight = maxJPEGHeight
	}
	if _, err := validateDesktopResolutionTarget(width, height, maxWidth, maxHeight); err != nil {
		return err
	}
	if err := s.conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type: protocol.DesktopSessionVideoControl,
		VideoControl: &protocol.DesktopVideoControl{
			TargetWidth:  width,
			TargetHeight: height,
		},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.followViewport = followViewport
	s.mu.Unlock()
	return nil
}

func (s *ControllerSession) RequestResolution(ctx context.Context, width, height int) error {
	return s.requestResolution(ctx, width, height, false)
}

func (s *ControllerSession) RequestViewportResolution(ctx context.Context, width, height int) error {
	return s.requestResolution(ctx, width, height, true)
}

func (s *ControllerSession) RequestDisplay(ctx context.Context, displayID string) error {
	if s == nil || !s.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	displayID = strings.TrimSpace(displayID)
	config := s.VideoConfigSnapshot()
	if displayID == config.DisplayID {
		return nil
	}
	if displayID == "" {
		switch s.options.CaptureBackend {
		case protocol.DesktopCaptureDXGI, protocol.DesktopCaptureWGC:
			return fmt.Errorf("%s capture requires selecting a specific display", s.options.CaptureBackend)
		}
	}
	return s.conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type: protocol.DesktopSessionVideoControl,
		VideoControl: &protocol.DesktopVideoControl{
			DisplayID: &displayID,
		},
	})
}

func (s *ControllerSession) RequestIDR(ctx context.Context) error {
	if s == nil || !s.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	s.recoveryMu.Lock()
	request := s.recovery.ForceRecovery()
	s.recoveryMu.Unlock()
	if !request {
		return nil
	}
	if err := s.sendIDRRequest(ctx); err != nil {
		s.markIDRRequestFailed()
		return err
	}
	return nil
}

func (s *ControllerSession) acceptVideoFrame(ctx context.Context, frame *desktopmedia.EncodedFrame, config protocol.DesktopVideoConfig, configured bool) bool {
	if !configured || !desktopAdaptiveVideoCodec(config.Codec) {
		s.recoveryMu.Lock()
		s.recovery.Reset()
		s.recoveryMu.Unlock()
		return true
	}
	s.recoveryMu.Lock()
	accept, request := s.recovery.Observe(frame, config)
	s.recoveryMu.Unlock()
	if request {
		requestCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := s.sendIDRRequest(requestCtx)
		cancel()
		if err != nil {
			s.markIDRRequestFailed()
			log.Printf("[Desktop] request %s IDR after frame loss failed: %v", config.Codec, err)
		}
	}
	return accept
}

func (s *ControllerSession) readLoop(ctx context.Context) {
	defer close(s.done)
	defer s.conn.Close()
	videoReassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{})
	audioReassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{
		PacketType: desktopmedia.MediaPacketAudio,
		MaxFrames:  16,
		MaxBytes:   4 << 20,
		FrameTTL:   250 * time.Millisecond,
	})
	for {
		packet, err := s.conn.Receive(ctx)
		if err != nil {
			return
		}
		header, _, decodeErr := desktopmedia.DecodeMediaPacket(packet)
		if decodeErr != nil {
			continue
		}
		if s.stats != nil {
			s.stats.ObservePacket(header, len(packet))
		}
		now := time.Now()
		if header.Type == desktopmedia.MediaPacketAudio {
			frame, pushErr := audioReassembler.Push(packet, now)
			if pushErr == nil && frame != nil {
				s.enqueueAudioFrame(frame)
			}
			continue
		}
		if header.Type != desktopmedia.MediaPacketVideo {
			continue
		}
		frame, err := videoReassembler.Push(packet, now)
		if err != nil || frame == nil {
			continue
		}
		config, configured := s.currentVideoConfig(ctx)
		if !frameMatchesVideoConfig(frame, config, configured) {
			if s.stats != nil {
				s.stats.ObserveDroppedFrame()
			}
			continue
		}
		if !s.acceptVideoFrame(ctx, frame, config, configured) {
			if s.stats != nil {
				s.stats.ObserveDroppedFrame()
			}
			continue
		}
		snapshot, ok := snapshotFromEncodedFrame(frame, config, configured)
		if !ok {
			continue
		}
		s.mu.Lock()
		s.latest = snapshot
		s.mu.Unlock()
		if s.stats != nil {
			s.stats.ObserveFrame()
		}
	}
}

func (s *ControllerSession) SetDatagramPath(path desktopmedia.DatagramPath) {
	if s == nil || s.conn == nil {
		if path != nil {
			_ = path.Close()
		}
		return
	}
	s.conn.SetDatagramPath(path)
	if s.stats != nil {
		s.stats.SetPath(s.conn.DatagramPathName())
	}
}

func (s *ControllerSession) ClearDatagramPath(path desktopmedia.DatagramPath) {
	if s == nil || s.conn == nil {
		return
	}
	s.conn.ClearDatagramPath(path)
	if s.stats != nil {
		s.stats.SetPath(s.conn.DatagramPathName())
	}
}

func (s *ControllerSession) DatagramPathName() string {
	if s == nil || s.conn == nil {
		return ""
	}
	return s.conn.DatagramPathName()
}

// PathQuality exposes media-path-local loss/queue metrics. When Relay is the
// active media path, its reliable session probe is also a useful Relay
// baseline. udp_p2p RTT/Jitter is supplied separately by the authenticated
// direct socket and is never inferred from this Relay probe.
func (s *ControllerSession) PathQuality() PathQuality {
	if s == nil || s.stats == nil {
		return PathQuality{}
	}
	now := time.Now()
	quality := s.stats.PathQuality(now, s.DatagramPathName() == "relay")
	if quality.Relay {
		snapshot := s.stats.Snapshot(now)
		quality.RTTMs = snapshot.RTTMs
		quality.JitterMs = snapshot.JitterMs
	}
	return quality
}

func (s *ControllerSession) Stats() protocol.DesktopSessionStats {
	if s == nil || s.stats == nil {
		return protocol.DesktopSessionStats{}
	}
	stats := s.stats.Snapshot(time.Now())
	if path := s.DatagramPathName(); path != "" {
		stats.Path = path
	}
	return stats
}

func (s *ControllerSession) Diagnostics() DesktopDiagnosticsReport {
	if s == nil || s.diagnostics == nil {
		return DesktopDiagnosticsReport{}
	}
	now := time.Now()
	stats := protocol.DesktopSessionStats{}
	if s.stats != nil {
		stats = s.stats.Snapshot(now)
		if path := s.DatagramPathName(); path != "" {
			stats.Path = path
		}
	}
	return s.diagnostics.Report(
		now,
		s.VideoConfigSnapshot(),
		stats,
		s.AudioDiagnosticsSnapshot(),
	)
}

func (s *ControllerSession) UpdateViewerStats(stats protocol.DesktopSessionStats) {
	if s == nil || s.stats == nil {
		return
	}
	s.stats.MergeViewer(stats)
}

func (s *ControllerSession) TargetID() string {
	if s == nil {
		return ""
	}
	return s.targetID
}

func (s *ControllerSession) Done() <-chan struct{} {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return s.done
}

func (s *ControllerSession) Active() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *ControllerSession) SendInput(ctx context.Context, event protocol.DesktopInputEvent) error {
	if s == nil || !s.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	if err := ValidateDesktopInputEvent(event); err != nil {
		return err
	}
	return s.conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type:  protocol.DesktopSessionInput,
		Input: &event,
	})
}

func (s *ControllerSession) SendClipboard(ctx context.Context, text string) error {
	if s == nil || !s.Active() {
		return errors.New("Relay Desktop session is not active")
	}
	text, err := validateClipboardText(text)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.clipboardSendSeq++
	sequence := s.clipboardSendSeq
	s.mu.Unlock()
	return s.conn.SendSessionMessage(ctx, protocol.DesktopSessionMessage{
		Type: protocol.DesktopSessionClipboard,
		Clipboard: &protocol.DesktopClipboardState{
			Sequence: sequence,
			Text:     text,
		},
	})
}

func (s *ControllerSession) LatestClipboard(knownSequence uint64) (protocol.DesktopClipboardState, bool) {
	if s == nil {
		return protocol.DesktopClipboardState{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latestClipboard.Sequence == 0 || s.latestClipboard.Sequence == knownSequence {
		return protocol.DesktopClipboardState{}, false
	}
	return s.latestClipboard, true
}

func (s *ControllerSession) LatestCursor(knownCursorID string) (protocol.DesktopCursorState, bool) {
	if s == nil {
		return protocol.DesktopCursorState{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latestCursor.Sequence == 0 {
		return protocol.DesktopCursorState{}, false
	}
	cursor := s.latestCursor
	if knownCursorID != "" && knownCursorID == cursor.CursorID {
		cursor.PNG = nil
	} else {
		cursor.PNG = append([]byte(nil), cursor.PNG...)
	}
	return cursor, true
}

func (s *ControllerSession) LatestFrame() (FrameSnapshot, bool) {
	if s == nil {
		return FrameSnapshot{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.latest.Sequence == 0 || len(s.latest.Data) == 0 {
		return FrameSnapshot{}, false
	}
	frame := s.latest
	frame.Data = append([]byte(nil), frame.Data...)
	return frame, true
}

func (s *ControllerSession) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.conn.Close()
	})
	<-s.done
	return nil
}
