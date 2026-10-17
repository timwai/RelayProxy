package divert

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestNetworkFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := networkFrame{
		kind:       networkFrameCapture,
		flags:      networkFrameFlagOutbound,
		ifIndex:    17,
		subIfIndex: 23,
		payload:    []byte{0x45, 0x00, 0x00, 0x14},
	}
	if err := writeNetworkFrame(&buffer, nil, want); err != nil {
		t.Fatal(err)
	}
	got, err := readNetworkFrame(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got.kind != want.kind || got.flags != want.flags ||
		got.ifIndex != want.ifIndex || got.subIfIndex != want.subIfIndex ||
		!bytes.Equal(got.payload, want.payload) {
		t.Fatalf("frame=%+v want=%+v", got, want)
	}
}

func TestNetworkFrameRejectsInvalidEnvelope(t *testing.T) {
	valid := make([]byte, networkFrameHeaderBytes)
	binary.LittleEndian.PutUint32(valid[0:4], networkPipeMagic)
	binary.LittleEndian.PutUint16(valid[4:6], networkPipeVersion)
	binary.LittleEndian.PutUint16(valid[6:8], networkFrameReady)

	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"magic", func(data []byte) { binary.LittleEndian.PutUint32(data[0:4], 0) }},
		{"version", func(data []byte) { binary.LittleEndian.PutUint16(data[4:6], networkPipeVersion+1) }},
		{"payload", func(data []byte) { binary.LittleEndian.PutUint32(data[20:24], networkFrameMaxPayload+1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), valid...)
			test.mutate(data)
			if _, err := readNetworkFrame(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid network frame was accepted")
			}
		})
	}
}

func TestNetworkFrameRejectsOversizedWrite(t *testing.T) {
	err := writeNetworkFrame(
		&bytes.Buffer{},
		nil,
		networkFrame{kind: networkFrameInject, payload: make([]byte, networkFrameMaxPayload+1)},
	)
	if err == nil {
		t.Fatal("oversized network frame write was accepted")
	}
}

func TestNetworkBrokerInjectsWithoutAnotherCapture(t *testing.T) {
	server, client := net.Pipe()
	device := newTestPacketDevice()
	done := make(chan error, 1)
	go func() { done <- serveNetworkPackets(server, device) }()
	defer func() { _ = client.Close(); <-done }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	device.incoming <- capturedTestPacket{data: []byte("capture"), meta: packetMetadata{outbound: true}}
	if _, err := readNetworkFrame(client); err != nil {
		t.Fatal(err)
	}
	if err := writeNetworkFrame(client, nil, networkFrame{kind: networkFrameComplete}); err != nil {
		t.Fatal(err)
	}
	// The service is now waiting in Receive. A delayed reply must still pass.
	if err := writeNetworkFrame(client, nil, networkFrame{kind: networkFrameInject, payload: []byte("reply"), ifIndex: 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-device.sent:
		if string(packet.data) != "reply" || packet.meta.ifIndex != 7 || packet.meta.outbound {
			t.Fatalf("wrong injection: %+v", packet)
		}
	case <-time.After(time.Second):
		t.Fatal("injection waited for another capture")
	}
}

func TestNetworkBrokerShutdownWhileWaitingForCompletion(t *testing.T) {
	server, client := net.Pipe()
	device := newTestPacketDevice()
	done := make(chan error, 1)
	go func() { done <- serveNetworkPackets(server, device) }()
	device.incoming <- capturedTestPacket{data: []byte("capture")}
	if _, err := readNetworkFrame(client); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broker leaked while awaiting completion")
	}
}

func TestNetworkBrokerReleasesCaptureForStalledClient(t *testing.T) {
	for _, readsCapture := range []bool{false, true} {
		t.Run(fmt.Sprintf("readsCapture=%t", readsCapture), func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			device := newTestPacketDevice()
			done := make(chan error, 1)
			go func() { done <- serveNetworkPacketsWithTimeout(server, device, 50*time.Millisecond) }()
			device.incoming <- capturedTestPacket{data: []byte("capture")}
			if readsCapture {
				_ = client.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := readNetworkFrame(client); err != nil {
					t.Fatal(err)
				}
			}
			// Keep the client alive, either refusing pipe reads or withholding
			// completion. Both must release the kernel without a client exit.
			select {
			case <-device.done:
			case <-time.After(time.Second):
				t.Fatal("stalled client retained machine-wide capture")
			}
			select {
			case err := <-done:
				if !errors.Is(err, errNetworkCaptureTimeout) {
					t.Fatalf("shutdown reason = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("watchdog left pipe IO blocked")
			}
		})
	}
}

func TestNetworkBrokerDoesNotTimeOutIdleOrCompletedCapture(t *testing.T) {
	server, client := net.Pipe()
	device := newTestPacketDevice()
	done := make(chan error, 1)
	go func() { done <- serveNetworkPacketsWithTimeout(server, device, 100*time.Millisecond) }()
	defer func() { _ = client.Close(); <-done }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	for n := 0; n < 3; n++ {
		// An empty capture source can remain idle beyond the handling limit,
		// both before the first packet and after a completed packet.
		select {
		case <-device.done:
			t.Fatal("idle capture was treated as a hung Agent")
		case <-time.After(150 * time.Millisecond):
		}
		device.incoming <- capturedTestPacket{data: []byte("capture")}
		if _, err := readNetworkFrame(client); err != nil {
			t.Fatal(err)
		}
		if err := writeNetworkFrame(client, nil, networkFrame{kind: networkFrameComplete}); err != nil {
			t.Fatal(err)
		}
	}
}
