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
	endErr error
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
	endErr := c.endErr
	c.mu.Unlock()
	if endErr != nil {
		return nil, endErr
	}
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

type audioRecordingDatagramPath struct {
	mu      sync.Mutex
	packets [][]byte
	closed  bool
}

func (p *audioRecordingDatagramPath) Name() string { return "test_direct" }

func (p *audioRecordingDatagramPath) Send(ctx context.Context, packet []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	p.packets = append(p.packets, append([]byte(nil), packet...))
	return nil
}

func (p *audioRecordingDatagramPath) Receive(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *audioRecordingDatagramPath) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

func (p *audioRecordingDatagramPath) snapshot() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	packets := make([][]byte, len(p.packets))
	for i := range p.packets {
		packets[i] = append([]byte(nil), p.packets[i]...)
	}
	return packets
}

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
	err = host.streamSessionAudio(context.Background(), conn, protocol.RemoteDesktopConnectOptions{})
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

func TestStreamSessionAudioEmitsConfigAndReassemblablePCM(t *testing.T) {
	cfg, duration, frameBytes, err := desktopaudio.NormalizeFrameDuration(hostAudioPCMConfig, hostAudioFrameDuration)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, frameBytes)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	capture := &fakeAudioCapture{
		frames: [][]byte{payload},
		endErr: io.EOF,
	}
	hostConfig := DefaultHostConfig()
	hostConfig.PacketSize = 600
	host := &Host{
		cfg: hostConfig,
		audioOpen: func(context.Context, desktopaudio.PCMConfig, time.Duration) (desktopaudio.Capture, error) {
			return capture, nil
		},
	}
	stream := &audioTestStream{}
	path := &audioRecordingDatagramPath{}
	conn := desktopmedia.NewMediaConn(nil, stream)
	conn.SetDatagramPath(path)

	err = host.streamSessionAudio(context.Background(), conn, protocol.RemoteDesktopConnectOptions{})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream error=%v want EOF after one synthetic frame", err)
	}

	stream.mu.Lock()
	controlBytes := bytes.Join(stream.messages, nil)
	stream.mu.Unlock()
	var message protocol.DesktopSessionMessage
	if err := protocol.ReadJSON(bytes.NewReader(controlBytes), &message); err != nil {
		t.Fatalf("decode audio config: %v", err)
	}
	if message.Type != protocol.DesktopSessionAudioConfig || message.AudioConfig == nil {
		t.Fatalf("session message=%+v", message)
	}
	audioConfig := *message.AudioConfig
	if audioConfig.Generation != 1 ||
		audioConfig.Codec != protocol.DesktopAudioCodecPCMS16LE ||
		audioConfig.SampleRate != cfg.SampleRate ||
		audioConfig.Channels != cfg.Channels ||
		audioConfig.BitsPerSample != cfg.BitsPerSample ||
		audioConfig.FrameDurationMs != int(duration/time.Millisecond) ||
		audioConfig.TargetBitrate != cfg.BytesPerSecond()*8 {
		t.Fatalf("audio config=%+v", audioConfig)
	}

	packets := path.snapshot()
	if len(packets) < 2 {
		t.Fatalf("packet count=%d want fragmented PCM frame", len(packets))
	}
	reassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{
		PacketType: desktopmedia.MediaPacketAudio,
		MaxFrames:  4,
		MaxBytes:   frameBytes * 2,
		FrameTTL:   time.Second,
	})
	var rebuilt *desktopmedia.EncodedFrame
	for i, packet := range packets {
		header, _, err := desktopmedia.DecodeMediaPacket(packet)
		if err != nil {
			t.Fatalf("decode packet %d: %v", i, err)
		}
		if header.Type != desktopmedia.MediaPacketAudio ||
			header.StreamID != desktopmedia.MediaStreamAudioID ||
			header.Generation != 1 ||
			header.FrameID != 1 ||
			header.Sequence != uint32(i+1) {
			t.Fatalf("audio packet %d header=%+v", i, header)
		}
		frame, err := reassembler.Push(packet, time.Now())
		if err != nil {
			t.Fatalf("reassemble packet %d: %v", i, err)
		}
		if frame != nil {
			rebuilt = frame
		}
	}
	if rebuilt == nil {
		t.Fatal("audio frame did not reassemble")
	}
	if rebuilt.Type != desktopmedia.MediaPacketAudio ||
		rebuilt.StreamID != desktopmedia.MediaStreamAudioID ||
		rebuilt.Generation != 1 ||
		rebuilt.FrameID != 1 ||
		!bytes.Equal(rebuilt.Data, payload) {
		t.Fatalf("rebuilt audio frame=%+v payloadEqual=%t", rebuilt, bytes.Equal(rebuilt.Data, payload))
	}

	capture.mu.Lock()
	closed := capture.closed
	capture.mu.Unlock()
	if !closed {
		t.Fatal("synthetic audio capture was not closed")
	}
}

func TestStreamSessionAudioEncodesNegotiatedOpus(t *testing.T) {
	cfg, duration, frameBytes, err := desktopaudio.NormalizeFrameDuration(hostAudioPCMConfig, hostAudioFrameDuration)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, frameBytes)
	for i := range payload {
		payload[i] = byte((i*17 + 23) % 251)
	}
	capture := &fakeAudioCapture{
		frames: [][]byte{payload},
		endErr: io.EOF,
	}
	hostConfig := DefaultHostConfig()
	hostConfig.PacketSize = 600
	host := &Host{
		cfg: hostConfig,
		audioOpen: func(context.Context, desktopaudio.PCMConfig, time.Duration) (desktopaudio.Capture, error) {
			return capture, nil
		},
	}
	stream := &audioTestStream{}
	path := &audioRecordingDatagramPath{}
	conn := desktopmedia.NewMediaConn(nil, stream)
	conn.SetDatagramPath(path)

	err = host.streamSessionAudio(context.Background(), conn, protocol.RemoteDesktopConnectOptions{
		AudioCodec: protocol.DesktopAudioCodecOpus,
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream error=%v want EOF after one synthetic Opus frame", err)
	}

	stream.mu.Lock()
	controlBytes := bytes.Join(stream.messages, nil)
	stream.mu.Unlock()
	var message protocol.DesktopSessionMessage
	if err := protocol.ReadJSON(bytes.NewReader(controlBytes), &message); err != nil {
		t.Fatalf("decode Opus audio config: %v", err)
	}
	if message.AudioConfig == nil {
		t.Fatal("Opus audio config is missing")
	}
	audioConfig := *message.AudioConfig
	if audioConfig.Codec != protocol.DesktopAudioCodecOpus ||
		audioConfig.TargetBitrate != desktopaudio.DefaultOpusBitrate ||
		audioConfig.SampleRate != cfg.SampleRate ||
		audioConfig.Channels != cfg.Channels ||
		audioConfig.FrameDurationMs != int(duration/time.Millisecond) {
		t.Fatalf("Opus audio config=%+v", audioConfig)
	}

	packets := path.snapshot()
	if len(packets) == 0 {
		t.Fatal("Opus audio emitted no datagrams")
	}
	reassembler := desktopmedia.NewReassembler(desktopmedia.ReassemblerConfig{
		PacketType: desktopmedia.MediaPacketAudio,
		MaxFrames:  4,
		MaxBytes:   frameBytes * 2,
		FrameTTL:   time.Second,
	})
	var rebuilt *desktopmedia.EncodedFrame
	for _, packet := range packets {
		frame, pushErr := reassembler.Push(packet, time.Now())
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if frame != nil {
			rebuilt = frame
		}
	}
	if rebuilt == nil || len(rebuilt.Data) == 0 || len(rebuilt.Data) >= len(payload) {
		t.Fatalf("rebuilt Opus frame=%+v PCM bytes=%d", rebuilt, len(payload))
	}

	decoder, err := desktopaudio.NewOpusDecoder(desktopaudio.OpusConfig{
		SampleRate:      audioConfig.SampleRate,
		Channels:        audioConfig.Channels,
		BitsPerSample:   audioConfig.BitsPerSample,
		FrameDurationMs: audioConfig.FrameDurationMs,
		Bitrate:         audioConfig.TargetBitrate,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decoder.DecodePacket(rebuilt.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != frameBytes {
		t.Fatalf("decoded Opus PCM bytes=%d want=%d", len(decoded), frameBytes)
	}
}
