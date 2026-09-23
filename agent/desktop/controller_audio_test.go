package desktop

import (
	"context"
	"errors"
	"testing"
	"time"

	desktopmedia "relayproxy/internal/desktop"
	"relayproxy/internal/protocol"
)

func testAudioConfig(generation uint32) protocol.DesktopAudioConfig {
	return protocol.DesktopAudioConfig{
		Generation:      generation,
		Codec:           protocol.DesktopAudioCodecPCMS16LE,
		SampleRate:      48_000,
		Channels:        2,
		BitsPerSample:   16,
		FrameDurationMs: 20,
		TargetBitrate:   1_536_000,
	}
}

func newAudioControllerTestSession() *ControllerSession {
	return &ControllerSession{
		done:        make(chan struct{}),
		audioNotify: make(chan struct{}, 1),
	}
}

func TestControllerAudioEnabledDefaultsOnAndHonorsExplicitDisable(t *testing.T) {
	if !newAudioControllerTestSession().AudioEnabled() {
		t.Fatal("audio should default to enabled")
	}
	disabled := false
	session := newAudioControllerTestSession()
	session.options.Audio = &disabled
	if session.AudioEnabled() {
		t.Fatal("explicit Audio=false was ignored")
	}
	if (*ControllerSession)(nil).AudioEnabled() {
		t.Fatal("nil session reported audio enabled")
	}
}

func TestApplyAudioConfigRejectsInvalidAndStaleGeneration(t *testing.T) {
	session := newAudioControllerTestSession()
	if session.applyAudioConfig(protocol.DesktopAudioConfig{}) {
		t.Fatal("invalid empty audio config was accepted")
	}
	if !session.applyAudioConfig(testAudioConfig(2)) {
		t.Fatal("valid audio config was rejected")
	}
	if session.applyAudioConfig(testAudioConfig(1)) {
		t.Fatal("stale audio generation was accepted")
	}
	mutated := testAudioConfig(2)
	mutated.SampleRate = 44_100
	if session.applyAudioConfig(mutated) {
		t.Fatal("same audio generation changed format without generation rollover")
	}
	if got := session.AudioConfigSnapshot(); got.Generation != 2 || got.SampleRate != 48_000 {
		t.Fatalf("audio config=%+v", got)
	}
}

func TestAudioFrameMatchesConfiguredStreamAndGeneration(t *testing.T) {
	config := testAudioConfig(4)
	valid := &desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		SessionID:  1,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 4,
		FrameID:    1,
		Data:       []byte{1},
	}
	if !audioFrameMatchesConfig(valid, config) {
		t.Fatal("valid audio frame was rejected")
	}
	stale := *valid
	stale.Generation = 3
	if audioFrameMatchesConfig(&stale, config) {
		t.Fatal("stale audio generation was accepted")
	}
	wrongStream := *valid
	wrongStream.StreamID = desktopmedia.MediaStreamVideoID
	if audioFrameMatchesConfig(&wrongStream, config) {
		t.Fatal("audio frame on video stream was accepted")
	}
}

func TestAudioQueueDropsOldestAndReturnsRealtimeTail(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testAudioConfig(1)) {
		t.Fatal("audio config rejected")
	}
	for i := 1; i <= maxControllerAudioFrames+2; i++ {
		if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			SessionID:  1,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: 1,
			FrameID:    uint32(i),
			Timestamp:  uint64(i * 20_000),
			Data:       []byte{byte(i)},
		}) {
			t.Fatalf("enqueue frame %d failed", i)
		}
	}
	frame, config, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := uint32(3); frame.FrameID != want {
		t.Fatalf("oldest retained frame=%d want=%d", frame.FrameID, want)
	}
	if config.Generation != 1 || len(frame.Data) != 1 || frame.Data[0] != 3 {
		t.Fatalf("frame=%+v config=%+v", frame, config)
	}
	frame.Data[0] = 99
	session.audioMu.Lock()
	if len(session.audioQueue) == 0 || session.audioQueue[0].Data[0] != 4 {
		t.Fatalf("returned frame mutated queued storage: %+v", session.audioQueue)
	}
	session.audioMu.Unlock()
}

func TestAudioGenerationSwitchClearsQueuedFrames(t *testing.T) {
	session := newAudioControllerTestSession()
	session.applyAudioConfig(testAudioConfig(1))
	if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		SessionID:  1,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 1,
		FrameID:    9,
		Data:       []byte{9},
	}) {
		t.Fatal("generation 1 frame enqueue failed")
	}
	if !session.applyAudioConfig(testAudioConfig(2)) {
		t.Fatal("generation 2 config rejected")
	}
	session.audioMu.Lock()
	queued := len(session.audioQueue)
	session.audioMu.Unlock()
	if queued != 0 {
		t.Fatalf("old generation queue not cleared: %d", queued)
	}
	if session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		SessionID:  1,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 1,
		FrameID:    10,
		Data:       []byte{10},
	}) {
		t.Fatal("stale generation frame was enqueued after config switch")
	}
}

func TestNextAudioFrameRespectsContextCancellation(t *testing.T) {
	session := newAudioControllerTestSession()
	session.applyAudioConfig(testAudioConfig(1))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err := session.NextAudioFrame(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v want deadline exceeded", err)
	}
}
