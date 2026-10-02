package resume

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var wire bytes.Buffer
	want := Frame{Type: FrameData, Flags: FlagFINAck, Seq: 42, Ack: 17, Payload: []byte("relayproxy")}
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

func TestReplayBufferReusesAcknowledgedStorage(t *testing.T) {
	buffer := NewReplayBuffer(MaxPayload)
	payload := make([]byte, MaxPayload)
	if _, err := buffer.Append(payload); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Ack(MaxPayload); err != nil {
		t.Fatal(err)
	}
	if cap(buffer.spare) != HeaderSize+MaxPayload {
		t.Fatalf("spare capacity=%d want=%d", cap(buffer.spare), HeaderSize+MaxPayload)
	}
	payload[0] = 42
	_, retained, _, err := buffer.appendRetained(payload)
	if err != nil {
		t.Fatal(err)
	}
	if buffer.spare != nil || retained[0] != 42 {
		t.Fatal("acknowledged storage was not reused safely")
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

type countingWriter struct {
	writes int
	buf    bytes.Buffer
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

func TestFrameWriterCombinesRetainedData(t *testing.T) {
	state, err := NewRandomStreamState(1024)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := state.dataRetained([]byte("combined"))
	if err != nil {
		t.Fatal(err)
	}
	var encoder frameWriter
	var output countingWriter
	if err := encoder.Write(&output, frame); err != nil {
		t.Fatal(err)
	}
	if output.writes != 1 {
		t.Fatalf("transport writes=%d want=1", output.writes)
	}
	decoded, err := ReadFrame(&output.buf)
	if err != nil || string(decoded.Payload) != "combined" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
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

func TestFrameRejectsUnknownFlagsAndReservedBytes(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteFrame(&wire, Frame{Type: FrameAck, Flags: 1 << 15}); !errors.Is(err, ErrFrame) {
		t.Fatalf("unknown write flags error=%v", err)
	}

	wire.Reset()
	if err := WriteFrame(&wire, Frame{Type: FrameAck}); err != nil {
		t.Fatal(err)
	}
	raw := wire.Bytes()
	raw[28] = 1
	if _, err := ReadFrame(bytes.NewReader(raw)); !errors.Is(err, ErrFrame) {
		t.Fatalf("non-zero reserved byte error=%v", err)
	}
}

func TestFrameReaderMixedFrames(t *testing.T) {
	frames := []Frame{
		{Type: FrameData, Seq: 1, Payload: []byte("small")},
		{Type: FrameData, Seq: 6, Payload: bytes.Repeat([]byte{0xa5}, MaxPayload)},
		{Type: FrameAck, Ack: 1 << 40, Flags: FlagFINAck},
		{Type: FrameData, Seq: MaxPayload + 6, Payload: []byte("tail")},
		{Type: FrameFIN, Seq: MaxPayload + 10},
	}
	var wire bytes.Buffer
	for _, frame := range frames {
		if err := WriteFrame(&wire, frame); err != nil {
			t.Fatal(err)
		}
	}
	var reader frameReader
	for _, want := range frames {
		got, err := reader.Read(&wire)
		if err != nil || got.Type != want.Type || got.Flags != want.Flags || got.Seq != want.Seq || got.Ack != want.Ack || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("frame mismatch: got=%+v want=%+v err=%v", got, want, err)
		}
	}
}

func TestFrameReaderRejectsMalformedFrames(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
		want   error
	}{
		{"magic", func(p []byte) []byte { p[0] = 0; return p }, ErrFrame},
		{"version", func(p []byte) []byte { p[4]++; return p }, ErrFrame},
		{"type", func(p []byte) []byte { p[5] = 255; return p }, ErrFrame},
		{"flags", func(p []byte) []byte { p[6] = 128; return p }, ErrFrame},
		{"reserved", func(p []byte) []byte { p[31] = 1; return p }, ErrFrame},
		{"oversize", func(p []byte) []byte { binary.BigEndian.PutUint32(p[24:28], MaxPayload+1); return p }, ErrPayloadSize},
		{"empty data", func(p []byte) []byte { clear(p[24:28]); return p }, ErrFrame},
		{"control payload", func(p []byte) []byte { p[5] = byte(FrameAck); return p }, ErrFrame},
		{"partial header", func(p []byte) []byte { return p[:HeaderSize-1] }, io.ErrUnexpectedEOF},
		{"partial payload", func(p []byte) []byte { return p[:len(p)-1] }, io.ErrUnexpectedEOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			var wire bytes.Buffer
			if err := WriteFrame(&wire, Frame{Type: FrameData, Payload: []byte("payload")}); err != nil {
				t.Fatal(err)
			}
			var reader frameReader
			if _, err := reader.Read(bytes.NewReader(test.mutate(wire.Bytes()))); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestReadFramePayloadRemainsOwnedByCaller(t *testing.T) {
	var wire bytes.Buffer
	for _, payload := range []string{"first", "later"} {
		if err := WriteFrame(&wire, Frame{Type: FrameData, Payload: []byte(payload)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadFrame(&wire)
	if err != nil {
		t.Fatal(err)
	}
	clear(second.Payload)
	if string(first.Payload) != "first" {
		t.Fatalf("subsequent frame overwrote caller-owned data: %q", first.Payload)
	}
}
