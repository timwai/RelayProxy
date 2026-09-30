package resume

import (
	"errors"
	"testing"
)

func TestStreamStateReplayDoesNotDuplicateDeliveredBytes(t *testing.T) {
	identity, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewStreamState(identity, 1024)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := NewStreamState(identity, 1024)
	if err != nil {
		t.Fatal(err)
	}

	first, err := sender.Data([]byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, ack, err := receiver.Handle(first)
	if err != nil || string(fresh) != "hello" || ack != 5 {
		t.Fatalf("first delivery fresh=%q ack=%d err=%v", fresh, ack, err)
	}

	ackFrame, _ := receiver.AckFrame()
	if _, _, err := sender.Handle(ackFrame); err != nil {
		t.Fatal(err)
	}
	if sender.BufferedReplayBytes() != 0 {
		t.Fatal("acknowledged bytes remain buffered")
	}

	second, err := sender.Data([]byte(" world"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, ack, err = receiver.Handle(second)
	if err != nil || string(fresh) != " world" || ack != 11 {
		t.Fatalf("second delivery fresh=%q ack=%d err=%v", fresh, ack, err)
	}
	// Simulate transport loss before the ACK reaches sender. The sender must
	// replay the unacknowledged bytes, while receiver must not deliver them twice.
	replay := sender.ReplayFrames()
	if len(replay) != 1 || replay[0].Seq != 5 {
		t.Fatalf("unexpected replay frames: %#v", replay)
	}
	fresh, ack, err = receiver.Handle(replay[0])
	if err != nil || len(fresh) != 0 || ack != 11 {
		t.Fatalf("duplicate replay delivered bytes fresh=%q ack=%d err=%v", fresh, ack, err)
	}
	finalAck, _ := receiver.AckFrame()
	if _, _, err := sender.Handle(finalAck); err != nil {
		t.Fatal(err)
	}
	if sender.BufferedReplayBytes() != 0 {
		t.Fatal("replayed bytes were not released after ACK")
	}
}

func TestStreamStateRejectsGapAndImpossibleAckWithoutReceiveAdvance(t *testing.T) {
	state, err := NewRandomStreamState(1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Data([]byte("outbound")); err != nil {
		t.Fatal(err)
	}
	before, _ := state.Binding(BindOpen, 1)

	_, _, err = state.Handle(Frame{Type: FrameData, Seq: 1, Ack: 0, Payload: []byte("gap")})
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("gap error=%v", err)
	}
	afterGap, _ := state.Binding(BindOpen, 1)
	if afterGap.ReceiveOffset != before.ReceiveOffset {
		t.Fatalf("gap advanced receive offset from %d to %d", before.ReceiveOffset, afterGap.ReceiveOffset)
	}

	_, _, err = state.Handle(Frame{Type: FrameData, Seq: 0, Ack: 99, Payload: []byte("valid-seq")})
	if !errors.Is(err, ErrFrame) {
		t.Fatalf("impossible ack error=%v", err)
	}
	afterAck, _ := state.Binding(BindOpen, 1)
	if afterAck.ReceiveOffset != before.ReceiveOffset {
		t.Fatalf("invalid ACK advanced receive offset from %d to %d", before.ReceiveOffset, afterAck.ReceiveOffset)
	}
}

func TestStreamStateBindingCapturesOffsets(t *testing.T) {
	state, err := NewRandomStreamState(1024)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := state.Data([]byte("send"))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Seq != 0 {
		t.Fatalf("initial send sequence=%d", frame.Seq)
	}
	if _, _, err := state.Handle(Frame{Type: FrameData, Seq: 0, Ack: 0, Payload: []byte("receive")}); err != nil {
		t.Fatal(err)
	}
	binding, err := state.Binding(BindOpen, 5)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Generation != 5 || binding.SendOffset != 4 || binding.ReceiveOffset != 7 {
		t.Fatalf("unexpected binding offsets: %+v", binding)
	}
}
