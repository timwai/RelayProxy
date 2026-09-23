package desktop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	desktopaudio "relayproxy/agent/desktop/audio"
	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

type fakeAudioCapabilitySource struct {
	CaptureSource
	available bool
}

func (s fakeAudioCapabilitySource) DesktopAudioAvailable() bool { return s.available }

type fakeAudioCapture struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
}

func (c *fakeAudioCapture) Read(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	if len(c.frames) > 0 {
		data := append([]byte(nil), c.frames[0]...)
		c.frames = c.frames[1:]
		c.mu.Unlock()
		return data, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *fakeAudioCapture) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

type audioTestStream struct {
	mu       sync.Mutex
	messages [][]byte
}

func (s *audioTestStream) Read([]byte) (int, error) { return 0, io.EOF }
func (s *audioTestStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.messages = append(s.messages, append([]byte(nil), p...))
	s.mu.Unlock()
	return len(p), nil
}
func (s *audioTestStream) Close() error                     { return nil }
func (s *audioTestStream) CloseWrite() error                { return nil }
func (s *audioTestStream) SetDeadline(time.Time) error      { return nil }
func (s *audioTestStream) SetReadDeadline(time.Time) error  { return nil }
func (s *audioTestStream) SetWriteDeadline(time.Time) error { return nil }

func TestDesktopAudioEnabledDefaultsOn(t *testing.T) {
	if !desktopAudioEnabled(protocol.RemoteDesktopConnectOptions{}) {
		t.Fatal("audio should default to enabled")
	}
	disabled := false
	if desktopAudioEnabled(protocol.RemoteDesktopConnectOptions{Audio: &disabled}) {
		t.Fatal("Audio=false was ignored")
	}
}

func TestHostDesktopAudioAvailableUsesSourceCapability(t *testing.T) {
	host := &Host{source: fakeAudioCapabilitySource{available: true}}
	if !host.desktopAudioAvailable() {
		t.Fatal("available source was not detected")
	}
	host.source = fakeAudioCapabilitySource{available: false}
	if host.desktopAudioAvailable() {
		t.Fatal("unavailable source was reported available")
	}
}

func TestStreamSessionAudioRejectsWrongFrameSize(t *testing.T) {
	cfg, _, frameBytes, err := desktopaudio.NormalizeFrameDuration(hostAudioPCMConfig, hostAudioFrameDuration)
	if err != nil {
		t.Fatal(err)
	}
	capture := &fakeAudioCapture{frames: [][]byte{bytes.Repeat([]byte{1}, frameBytes-cfg.BlockAlign())}}
	host := &Host{
		cfg: DefaultHostConfig(),
		audioOpen: func(context.Context, desktopaudio.PCMConfig, time.Duration) (desktopaudio.Capture, error) {
			return capture, nil
		},
	}
	stream := &audioTestStream{}
	conn := desktopmedia.NewMediaConn(nil, stream)
	err = host.streamSessionAudio(context.Background(), conn)
	if err == nil {
		t.Fatal("short audio capture frame was accepted")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	capture.mu.Lock()
	closed := capture.closed
	capture.mu.Unlock()
	if !closed {
		t.Fatal("audio capture was not closed after stream failure")
	}
}
