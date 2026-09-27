package desktop

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestPacketizeAndReassembleOutOfOrder(t *testing.T) {
	data := bytes.Repeat([]byte("relay-desktop-"), 700)
	frame := EncodedFrame{
		SessionID:  11,
		StreamID:   1,
		Generation: 2,
		FrameID:    9,
		Timestamp:  1234,
		KeyFrame:   true,
		Data:       data,
	}
	packets, next, err := PacketizeFrame(frame, 1200, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) < 2 || next != 100+uint32(len(packets)) {
		t.Fatalf("packets=%d next=%d", len(packets), next)
	}
	for _, packet := range packets {
		if len(packet) > 1200 {
			t.Fatalf("packet exceeds MTU: %d", len(packet))
		}
	}
	slices.Reverse(packets)
	reassembler := NewReassembler(ReassemblerConfig{})
	var completed *EncodedFrame
	now := time.Unix(1, 0)
	for _, packet := range packets {
		got, pushErr := reassembler.Push(packet, now)
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if got != nil {
			completed = got
		}
	}
	if completed == nil {
		t.Fatal("frame was not completed")
	}
	if !completed.KeyFrame || completed.Generation != frame.Generation || !bytes.Equal(completed.Data, data) {
		t.Fatalf("completed frame mismatch: %+v", completed)
	}
}

func TestReassemblerDuplicateFragmentIsIdempotent(t *testing.T) {
	frame := EncodedFrame{SessionID: 1, StreamID: 1, Generation: 1, FrameID: 1, Data: bytes.Repeat([]byte{1}, 2000)}
	packets, _, err := PacketizeFrame(frame, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := NewReassembler(ReassemblerConfig{})
	now := time.Unix(2, 0)
	if got, err := r.Push(packets[0], now); err != nil || got != nil {
		t.Fatalf("first push = %+v, %v", got, err)
	}
	if got, err := r.Push(packets[0], now); err != nil || got != nil {
		t.Fatalf("duplicate push = %+v, %v", got, err)
	}
	var completed *EncodedFrame
	for _, packet := range packets[1:] {
		got, pushErr := r.Push(packet, now)
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if got != nil {
			completed = got
		}
	}
	if completed == nil || len(completed.Data) != len(frame.Data) {
		t.Fatal("duplicate fragment prevented completion")
	}
}

func TestReassemblerRejectsInconsistentFrame(t *testing.T) {
	frame := EncodedFrame{SessionID: 2, StreamID: 1, Generation: 1, FrameID: 2, Timestamp: 10, Data: bytes.Repeat([]byte{2}, 1800)}
	packets, _, err := PacketizeFrame(frame, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	r := NewReassembler(ReassemblerConfig{})
	now := time.Unix(3, 0)
	if _, err := r.Push(packets[0], now); err != nil {
		t.Fatal(err)
	}
	header, payload, err := DecodeMediaPacket(packets[1])
	if err != nil {
		t.Fatal(err)
	}
	header.Timestamp++
	bad, err := EncodeMediaPacket(header, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Push(bad, now); !errors.Is(err, ErrMediaPacket) {
		t.Fatalf("error = %v, want ErrMediaPacket", err)
	}
}

func TestReassemblerExpiresPartialFrames(t *testing.T) {
	first := EncodedFrame{SessionID: 3, StreamID: 1, Generation: 1, FrameID: 1, Data: bytes.Repeat([]byte{3}, 1600)}
	second := EncodedFrame{SessionID: 3, StreamID: 1, Generation: 1, FrameID: 2, Data: []byte("new")}
	packets, _, err := PacketizeFrame(first, 1000, 1)
	if err != nil {
		t.Fatal(err)
	}
	newPackets, _, err := PacketizeFrame(second, 1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	r := NewReassembler(ReassemblerConfig{MaxFrames: 1, FrameTTL: 10 * time.Millisecond})
	start := time.Unix(4, 0)
	if _, err := r.Push(packets[0], start); err != nil {
		t.Fatal(err)
	}
	got, err := r.Push(newPackets[0], start.Add(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || string(got.Data) != "new" {
		t.Fatalf("completed = %+v", got)
	}
}

func TestReassemblerKeepsGenerationsSeparate(t *testing.T) {
	first := EncodedFrame{
		SessionID: 7, StreamID: 1, Generation: 1, FrameID: 1,
		Data: bytes.Repeat([]byte("old"), 500),
	}
	second := EncodedFrame{
		SessionID: 7, StreamID: 1, Generation: 2, FrameID: 1,
		KeyFrame: true, Data: bytes.Repeat([]byte("new"), 500),
	}
	firstPackets, _, err := PacketizeFrame(first, 800, 1)
	if err != nil {
		t.Fatal(err)
	}
	secondPackets, _, err := PacketizeFrame(second, 800, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPackets) < 2 || len(secondPackets) < 2 {
		t.Fatal("test frames must span multiple packets")
	}

	r := NewReassembler(ReassemblerConfig{})
	now := time.Unix(5, 0)
	if got, err := r.Push(firstPackets[0], now); err != nil || got != nil {
		t.Fatalf("old generation first fragment = %+v, %v", got, err)
	}
	if got, err := r.Push(secondPackets[0], now); err != nil || got != nil {
		t.Fatalf("new generation first fragment = %+v, %v", got, err)
	}

	var completedNew *EncodedFrame
	for _, packet := range secondPackets[1:] {
		got, pushErr := r.Push(packet, now)
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if got != nil {
			completedNew = got
		}
	}
	if completedNew == nil || completedNew.Generation != 2 || completedNew.FrameID != 1 ||
		!bytes.Equal(completedNew.Data, second.Data) {
		t.Fatalf("new generation frame=%+v", completedNew)
	}

	var completedOld *EncodedFrame
	for _, packet := range firstPackets[1:] {
		got, pushErr := r.Push(packet, now)
		if pushErr != nil {
			t.Fatal(pushErr)
		}
		if got != nil {
			completedOld = got
		}
	}
	if completedOld == nil || completedOld.Generation != 1 || completedOld.FrameID != 1 ||
		!bytes.Equal(completedOld.Data, first.Data) {
		t.Fatalf("old generation frame=%+v", completedOld)
	}
}
