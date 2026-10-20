package gateway

import (
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func TestRDPTCPRelaySurvivesQuietMainChannel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		upstream, targetPeer := net.Pipe()
		defer targetPeer.Close()
		left, client := net.Pipe()
		defer client.Close()
		manager := session.NewManager()
		controller := &session.DeviceSession{DeviceID: "controller", Grants: []string{protocol.CapabilityRDPClient}}
		target := &session.DeviceSession{DeviceID: "target", Grants: []string{protocol.CapabilityRDPHost}, Tunnel: &policyTestSession{stream: &policyTestStream{upstream}}}
		manager.Register(controller)
		manager.Register(target)
		router := NewStreamRouter(manager, nil, nil, nil)
		router.SetRDPChecker(func(string, string) (bool, error) { return true, nil })
		done := make(chan struct{})
		go func() {
			router.handleOpenRDPTCP(ctx, &protocol.StreamHeader{ExitDeviceID: "target"}, &policyTestStream{left}, controller, time.Now().Add(15*time.Second))
			close(done)
		}()
		handshake := make(chan error, 1)
		go func() {
			if _, err := protocol.ReadStreamHeader(targetPeer); err != nil {
				handshake <- err
				return
			}
			var request protocol.OpenRDPRequest
			if err := protocol.ReadJSON(targetPeer, &request); err != nil {
				handshake <- err
				return
			}
			handshake <- protocol.WriteJSON(targetPeer, protocol.OpenTCPResponse{RequestID: request.RequestID, Success: true})
		}()
		if err := protocol.WriteJSON(client, protocol.OpenRDPRequest{RequestID: "rdp", TimeoutMs: 10000}); err != nil {
			t.Fatal(err)
		}
		var response protocol.OpenTCPResponse
		if err := protocol.ReadJSON(client, &response); err != nil || !response.Success {
			t.Fatalf("RDP handshake failed: response=%+v err=%v", response, err)
		}
		if err := <-handshake; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(31 * time.Minute)
		for _, pair := range [][2]net.Conn{{client, targetPeer}, {targetPeer, client}} {
			written := make(chan error, 1)
			go func() { _, err := pair[0].Write([]byte("rdp")); written <- err }()
			var payload [3]byte
			if _, err := io.ReadFull(pair[1], payload[:]); err != nil {
				t.Fatalf("relay closed a quiet RDP TCP channel: %v", err)
			}
			if err := <-written; err != nil || string(payload[:]) != "rdp" {
				t.Fatalf("RDP transfer after idle: payload=%q err=%v", payload, err)
			}
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("relay did not stop on session cancellation")
		}
		if controller.ActiveStreams.Load() != 0 || target.ActiveStreams.Load() != 0 || controller.BytesUp.Load() != 3 || controller.BytesDown.Load() != 3 {
			t.Fatal("RDP stream accounting was lost")
		}
	})
}
