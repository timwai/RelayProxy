package desktop

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"log"
	"sync"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type DesktopMediaDialer func(context.Context, string) (*desktopmedia.MediaConn, error)

type FrameSnapshot struct {
	Sequence  uint64
	MimeType  string
	Codec     string
	Width     int
	Height    int
	Timestamp uint64
	KeyFrame  bool
	Data      []byte
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
	configReady      chan struct{}
	configOnce       sync.Once

	recoveryMu sync.Mutex
	recovery   h264RecoveryState

	stats *sessionStatsTracker
}

func StartController(parent context.Context, targetID string, dial DesktopMediaDialer) (*ControllerSession, error) {
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
	session := &ControllerSession{
		targetID:    targetID,
		conn:        conn,
		cancel:      cancel,
		done:        make(chan struct{}),
		configReady: make(chan struct{}),
		stats:       newSessionStatsTracker("relay"),
	}
	go session.controlLoop(ctx)
	go session.probeLoop(ctx)
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
			s.mu.Lock()
			s.videoConfig = *message.VideoConfig
			s.mu.Unlock()
			s.configOnce.Do(func() { close(s.configReady) })

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

		case protocol.DesktopSessionStats:
			if message.Stats != nil && s.stats != nil {
				s.stats.MergeRemote(*message.Stats)
			}
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

func snapshotFromEncodedFrame(frame *desktopmedia.EncodedFrame, config protocol.DesktopVideoConfig, configured bool) (FrameSnapshot, bool) {
	if frame == nil {
		return FrameSnapshot{}, false
	}
	snapshot := FrameSnapshot{
		Sequence:  uint64(frame.FrameID),
		Timestamp: frame.Timestamp,
		KeyFrame:  frame.KeyFrame,
		Data:      append([]byte(nil), frame.Data...),
	}
	if configured && config.Codec == "h264" {
		snapshot.MimeType = "video/h264"
		snapshot.Codec = config.CodecString
		snapshot.Width = config.Width
		snapshot.Height = config.Height
		return snapshot, true
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
	if !configured || config.Codec != "h264" {
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
			log.Printf("[Desktop] request H.264 IDR after frame loss failed: %v", err)
		}
	}
	return accept
}

func (s *ControllerSession) readLoop(ctx context.Context) {
	defer close(s.done)
	defer s.conn.Close()
	reassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{})
	for {
		packet, err := s.conn.Receive(ctx)
		if err != nil {
			return
		}
		if s.stats != nil {
			if header, _, decodeErr := desktopmedia.DecodeMediaPacket(packet); decodeErr == nil {
				s.stats.ObservePacket(header, len(packet))
			}
		}
		frame, err := reassembler.Push(packet, time.Now())
		if err != nil || frame == nil {
			continue
		}
		config, configured := s.currentVideoConfig(ctx)
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

func (s *ControllerSession) Stats() protocol.DesktopSessionStats {
	if s == nil || s.stats == nil {
		return protocol.DesktopSessionStats{}
	}
	return s.stats.Snapshot(time.Now())
}

func (s *ControllerSession) TargetID() string {
	if s == nil {
		return ""
	}
	return s.targetID
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
