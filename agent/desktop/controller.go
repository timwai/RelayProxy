package desktop

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"sync"
	"time"

	desktopmedia "relayproxy/internal/desktop"
)

type DesktopMediaDialer func(context.Context, string) (*desktopmedia.MediaConn, error)

type FrameSnapshot struct {
	Sequence uint64
	MimeType string
	Width    int
	Height   int
	Data     []byte
}

type ControllerSession struct {
	targetID string
	conn     *desktopmedia.MediaConn
	cancel   context.CancelFunc
	done     chan struct{}

	closeOnce sync.Once
	mu        sync.RWMutex
	latest    FrameSnapshot
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
		done:     make(chan struct{}),
	}
	go session.readLoop(ctx)
	return session, nil
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
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(frame.Data))
		if err != nil {
			continue
		}
		s.mu.Lock()
		s.latest = FrameSnapshot{
			Sequence: uint64(frame.FrameID),
			MimeType: "image/jpeg",
			Width:    cfg.Width,
			Height:   cfg.Height,
			Data:     append([]byte(nil), frame.Data...),
		}
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
