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

func TestAudioDiagnosticsTrackQueueDropsAndConsumption(t *testing.T) {
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
			Data:       []byte{byte(i), 0, byte(i), 0},
		}) {
			t.Fatalf("enqueue frame %d failed", i)
		}
	}
	before := session.AudioDiagnosticsSnapshot()
	if !before.Enabled || before.Config.Generation != 1 {
		t.Fatalf("audio diagnostics config=%+v", before)
	}
	if before.QueueFrames != maxControllerAudioFrames ||
		before.QueueCapacity != maxControllerAudioFrames ||
		before.ReceivedFrames != maxControllerAudioFrames+2 ||
		before.ReceivedBytes != uint64((maxControllerAudioFrames+2)*4) ||
		before.QueueDroppedFrames != 2 ||
		before.ConsumedFrames != 0 ||
		before.LastFrameID != maxControllerAudioFrames+2 ||
		before.LastMediaTimestampUS != uint64((maxControllerAudioFrames+2)*20_000) ||
		before.LastReceivedAtUnixMs == 0 {
		t.Fatalf("audio diagnostics before consume=%+v", before)
	}

	frame, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.FrameID != 3 {
		t.Fatalf("consumed frame=%d want=3", frame.FrameID)
	}
	after := session.AudioDiagnosticsSnapshot()
	if after.QueueFrames != maxControllerAudioFrames-1 ||
		after.ConsumedFrames != 1 ||
		after.ConsumedBytes != 4 ||
		after.LastConsumedAtUnixMs == 0 {
		t.Fatalf("audio diagnostics after consume=%+v", after)
	}
}

func TestAudioDiagnosticsTrackRejectedAndGenerationDiscardedFrames(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testAudioConfig(1)) {
		t.Fatal("audio config rejected")
	}
	if session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 2,
		FrameID:    1,
		Data:       []byte{1, 0, 1, 0},
	}) {
		t.Fatal("future generation frame was accepted")
	}
	if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 1,
		FrameID:    2,
		Data:       []byte{2, 0, 2, 0},
	}) {
		t.Fatal("valid generation frame was rejected")
	}
	if !session.applyAudioConfig(testAudioConfig(2)) {
		t.Fatal("generation 2 config rejected")
	}
	got := session.AudioDiagnosticsSnapshot()
	if got.RejectedFrames != 1 || got.GenerationDiscardedFrames != 1 || got.QueueFrames != 0 {
		t.Fatalf("audio diagnostics=%+v", got)
	}
}

func TestAudioQueueReordersFramesBeforePlayout(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testAudioConfig(1)) {
		t.Fatal("audio config rejected")
	}
	for _, id := range []uint32{2, 1} {
		if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: 1,
			FrameID:    id,
			Timestamp:  uint64(id) * 20_000,
			Data:       []byte{byte(id), 0, byte(id), 0},
		}) {
			t.Fatalf("enqueue frame %d failed", id)
		}
	}
	frame, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.FrameID != 1 {
		t.Fatalf("first playout frame=%d want=1", frame.FrameID)
	}
	got := session.AudioDiagnosticsSnapshot()
	if got.ReorderedFrames != 1 || got.LastConsumedFrameID != 1 {
		t.Fatalf("audio diagnostics=%+v", got)
	}
}

func TestAudioQueueRejectsDuplicateAndLateFrames(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testAudioConfig(1)) {
		t.Fatal("audio config rejected")
	}
	frame := func(id uint32) *desktopmedia.EncodedFrame {
		return &desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: 1,
			FrameID:    id,
			Timestamp:  uint64(id) * 20_000,
			Data:       []byte{byte(id), 0, byte(id), 0},
		}
	}
	if !session.enqueueAudioFrame(frame(1)) || !session.enqueueAudioFrame(frame(2)) {
		t.Fatal("initial frames rejected")
	}
	if session.enqueueAudioFrame(frame(2)) {
		t.Fatal("duplicate queued frame was accepted")
	}
	first, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.FrameID != 1 {
		t.Fatalf("first frame=%d want=1", first.FrameID)
	}
	if session.enqueueAudioFrame(frame(1)) {
		t.Fatal("late frame older than playout head was accepted")
	}
	got := session.AudioDiagnosticsSnapshot()
	if got.DuplicateFrames != 1 || got.LateFrames != 1 || got.RejectedFrames != 2 {
		t.Fatalf("audio diagnostics=%+v", got)
	}
}

func TestAudioSingleFrameExpiresBoundedPlayoutWait(t *testing.T) {
	session := newAudioControllerTestSession()
	cfg := testAudioConfig(1)
	cfg.FrameDurationMs = 20
	if !session.applyAudioConfig(cfg) {
		t.Fatal("audio config rejected")
	}
	if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
		Type:       desktopmedia.MediaPacketAudio,
		StreamID:   desktopmedia.MediaStreamAudioID,
		Generation: 1,
		FrameID:    1,
		Timestamp:  20_000,
		Data:       []byte{1, 0, 1, 0},
	}) {
		t.Fatal("audio frame rejected")
	}
	session.audioMu.Lock()
	session.audioQueue[0].receivedAt = time.Now().Add(-time.Second)
	session.audioMu.Unlock()

	frame, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.FrameID != 1 {
		t.Fatalf("frame=%d want=1", frame.FrameID)
	}
	got := session.AudioDiagnosticsSnapshot()
	if got.PlayoutTimeoutFrames != 1 {
		t.Fatalf("playout timeout frames=%d want=1", got.PlayoutTimeoutFrames)
	}
}

func TestAudioPlayoutDelayIsBounded(t *testing.T) {
	tests := []struct {
		frameMs int
		want    time.Duration
	}{
		{frameMs: 0, want: 40 * time.Millisecond},
		{frameMs: 5, want: 20 * time.Millisecond},
		{frameMs: 20, want: 40 * time.Millisecond},
		{frameMs: 100, want: 80 * time.Millisecond},
	}
	for _, tc := range tests {
		cfg := testAudioConfig(1)
		cfg.FrameDurationMs = tc.frameMs
		if got := audioPlayoutDelay(cfg); got != tc.want {
			t.Fatalf("frameMs=%d delay=%s want=%s", tc.frameMs, got, tc.want)
		}
	}
}

func testOpusAudioConfig(generation uint32) protocol.DesktopAudioConfig {
	cfg := testAudioConfig(generation)
	cfg.Codec = protocol.DesktopAudioCodecOpus
	cfg.TargetBitrate = 96_000
	return cfg
}

func TestOpusAudioGapEmitsConcealmentBeforeFutureFrame(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testOpusAudioConfig(1)) {
		t.Fatal("Opus audio config rejected")
	}
	for _, id := range []uint32{1, 3, 4} {
		if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: 1,
			FrameID:    id,
			Timestamp:  uint64(id) * 20_000,
			Data:       []byte{byte(id)},
		}) {
			t.Fatalf("enqueue frame %d failed", id)
		}
	}

	first, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.FrameID != 1 || first.Concealment {
		t.Fatalf("first frame=%+v", first)
	}

	plc, config, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !plc.Concealment || plc.FrameID != 2 || len(plc.Data) != 0 ||
		plc.Timestamp != 40_000 || config.Codec != protocol.DesktopAudioCodecOpus {
		t.Fatalf("PLC frame=%+v config=%+v", plc, config)
	}

	next, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if next.FrameID != 3 || next.Concealment {
		t.Fatalf("next frame=%+v", next)
	}

	got := session.AudioDiagnosticsSnapshot()
	if got.ConcealmentFrames != 1 || got.GapSkippedFrames != 0 ||
		got.ConsumedFrames != 2 || got.LastConsumedFrameID != 3 {
		t.Fatalf("audio diagnostics=%+v", got)
	}
}

func TestOpusAudioConcealmentBurstIsBounded(t *testing.T) {
	session := newAudioControllerTestSession()
	if !session.applyAudioConfig(testOpusAudioConfig(1)) {
		t.Fatal("Opus audio config rejected")
	}
	for _, id := range []uint32{10, 11} {
		if !session.enqueueAudioFrame(&desktopmedia.EncodedFrame{
			Type:       desktopmedia.MediaPacketAudio,
			StreamID:   desktopmedia.MediaStreamAudioID,
			Generation: 1,
			FrameID:    id,
			Timestamp:  uint64(id) * 20_000,
			Data:       []byte{byte(id)},
		}) {
			t.Fatalf("enqueue frame %d failed", id)
		}
	}

	for want := uint32(1); want <= maxControllerAudioConcealmentFrames; want++ {
		frame, _, err := session.NextAudioFrame(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !frame.Concealment || frame.FrameID != want {
			t.Fatalf("concealment frame=%+v want id=%d", frame, want)
		}
	}
	frame, _, err := session.NextAudioFrame(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if frame.Concealment || frame.FrameID != 10 {
		t.Fatalf("post-cap frame=%+v want live frame 10", frame)
	}
	got := session.AudioDiagnosticsSnapshot()
	if got.ConcealmentFrames != maxControllerAudioConcealmentFrames ||
		got.GapSkippedFrames != 6 ||
		got.ConsumedFrames != 1 ||
		got.LastConsumedFrameID != 10 {
		t.Fatalf("audio diagnostics=%+v", got)
	}
}
