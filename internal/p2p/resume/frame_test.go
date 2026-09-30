package resume

import (
	"bytes"
	"errors"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	want := Frame{Type: FrameData, Seq: 42, Ack: 17, Payload: []byte("relayproxy")}
	if err := WriteFrame(&wire, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != want.Type || got.Seq != want.Seq || got.Ack != want.Ack || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("frame mismatch: got=%+v want=%+v", got, want)
	}
}

func TestFramePreserves64BitAck(t *testing.T) {
	var wire bytes.Buffer
	want := Frame{Type: FrameAck, Seq: 1 << 40, Ack: (1 << 40) + 123}
	if err := WriteFrame(&wire, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != want.Seq || got.Ack != want.Ack {
		t.Fatalf("64-bit offsets truncated: got=%+v want=%+v", got, want)
	}
}

func TestFrameRejectsOversizedPayload(t *testing.T) {
	var wire bytes.Buffer
	err := WriteFrame(&wire, Frame{Type: FrameData, Payload: make([]byte, MaxPayload+1)})
	if !errors.Is(err, ErrPayloadSize) {
		t.Fatalf("oversized payload error=%v", err)
	}
}

func TestReplayBufferPartialAckAndSnapshot(t *testing.T) {
	buffer := NewReplayBuffer(64)
	seq0, err := buffer.Append([]byte("abcdef"))
	if err != nil || seq0 != 0 {
		t.Fatalf("first append seq=%d err=%v", seq0, err)
	}
	seq1, err := buffer.Append([]byte("ghij"))
	if err != nil || seq1 != 6 {
		t.Fatalf("second append seq=%d err=%v", seq1, err)
	}
	if err := buffer.Ack(4); err != nil {
		t.Fatal(err)
	}
	frames := buffer.Snapshot()
	if len(frames) != 2 || frames[0].Seq != 4 || string(frames[0].Payload) != "ef" || frames[1].Seq != 6 {
		t.Fatalf("unexpected replay snapshot: %#v", frames)
	}
	if buffer.Buffered() != 6 || buffer.Acked() != 4 || buffer.Next() != 10 {
		t.Fatalf("unexpected replay counters buffered=%d acked=%d next=%d", buffer.Buffered(), buffer.Acked(), buffer.Next())
	}
	if err := buffer.Ack(10); err != nil {
		t.Fatal(err)
	}
	if buffer.Buffered() != 0 || len(buffer.Snapshot()) != 0 {
		t.Fatal("fully acknowledged replay data was retained")
	}
}

func TestReplayBufferRejectsImpossibleAckAndLimit(t *testing.T) {
	buffer := NewReplayBuffer(4)
	if _, err := buffer.Append([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Append([]byte("5")); !errors.Is(err, ErrReplayLimit) {
		t.Fatalf("limit error=%v", err)
	}
	if err := buffer.Ack(5); !errors.Is(err, ErrFrame) {
		t.Fatalf("impossible ack error=%v", err)
	}
}

func TestReceiverDeduplicatesReplayAndRejectsGap(t *testing.T) {
	var receiver Receiver
	fresh, ack, err := receiver.Accept(0, []byte("hello"))
	if err != nil || string(fresh) != "hello" || ack != 5 {
		t.Fatalf("first receive fresh=%q ack=%d err=%v", fresh, ack, err)
	}
	fresh, ack, err = receiver.Accept(0, []byte("hello world"))
	if err != nil || string(fresh) != " world" || ack != 11 {
		t.Fatalf("replay receive fresh=%q ack=%d err=%v", fresh, ack, err)
	}
	fresh, ack, err = receiver.Accept(0, []byte("hello"))
	if err != nil || len(fresh) != 0 || ack != 11 {
		t.Fatalf("duplicate receive fresh=%q ack=%d err=%v", fresh, ack, err)
	}
	if _, ack, err = receiver.Accept(12, []byte("!")); !errors.Is(err, ErrSequenceGap) || ack != 11 {
		t.Fatalf("gap ack=%d err=%v", ack, err)
	}
}

type shortWriter struct {
	max int
	buf bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.max {
		p = p[:w.max]
	}
	return w.buf.Write(p)
}

func TestWriteFrameHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{max: 3}
	want := Frame{Type: FrameData, Seq: 9, Ack: 7, Payload: []byte("short-write-safe")}
	if err := WriteFrame(writer, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&writer.buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Seq != want.Seq || got.Ack != want.Ack || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("short-write round trip mismatch: got=%+v want=%+v", got, want)
	}
}
