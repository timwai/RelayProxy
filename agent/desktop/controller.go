package desktop

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
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

	closeOnce sync.Once
	mu          sync.RWMutex
	latest      FrameSnapshot
	videoConfig protocol.DesktopVideoConfig
	configReady chan struct{}
	configOnce  sync.Once
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
		targetID: targetID,
		conn:     conn,
		cancel:   cancel,
		done:        make(chan struct{}),
		configReady: make(chan struct{}),
	}
	go session.controlLoop(ctx)
	go session.readLoop(ctx)
	return session, nil
}

func (s *ControllerSession) controlLoop(ctx context.Context) {
	for {
		message, err := s.conn.ReceiveSessionMessage(ctx)
		if err != nil {
			return
		}
		if message.Type != protocol.DesktopSessionVideoConfig || message.VideoConfig == nil {
			continue
		}
		s.mu.Lock()
		s.videoConfig = *message.VideoConfig
		s.mu.Unlock()
		s.configOnce.Do(func() { close(s.configReady) })
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

func (s *ControllerSession) readLoop(ctx context.Context) {
	defer close(s.done)
	defer s.conn.Close()
	reassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{})
	for {
		packet, err := s.conn.Receive(ctx)
		if err != nil {
			return
		}
		frame, err := reassembler.Push(packet, time.Now())
		if err != nil || frame == nil {
			continue
		}
		config, configured := s.currentVideoConfig(ctx)
		snapshot, ok := snapshotFromEncodedFrame(frame, config, configured)
		if !ok {
			continue
		}
		s.mu.Lock()
		s.latest = snapshot
		s.mu.Unlock()
	}
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
