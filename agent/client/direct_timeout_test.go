package client

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestSlowTargetTimeoutPreservesSharedDirectSession(t *testing.T) {
	direct, remote := dialerQUICPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	served := make(chan error, 1)
	existingPeer := make(chan tunnel.TunnelStream, 1)
	go func() {
		for i := 0; i < 2; i++ {
			stream, err := remote.AcceptStream(ctx)
			if err != nil {
				served <- err
				return
			}
			if _, err = protocol.ReadStreamHeader(stream); err != nil {
				served <- err
				return
			}
			var req protocol.OpenTCPRequest
			if err = protocol.ReadJSON(stream, &req); err != nil {
				served <- err
				return
			}
			if i == 1 {
				// The healthy tunnel has delivered the request; target DNS/connect is slow.
				select {
				case <-time.After(150 * time.Millisecond):
				case <-ctx.Done():
				}
			}
			err = protocol.WriteJSON(stream, protocol.OpenTCPResponse{Success: true, RemoteIP: "203.0.113.10"})
			if i == 0 {
				if err != nil {
					served <- err
					return
				}
				existingPeer <- stream
			}
		}
		served <- nil
	}()
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{Success: true, RemoteIP: "203.0.113.10"})
	})
	d := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	d.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
	d.ConfigureDirectAttemptTimeout(50 * time.Millisecond)
	var invalidated atomic.Bool
	d.ConfigurePathFailure(func(_ string, _ protocol.ProxyPath, reason string) {
		invalidated.Store(true)
		t.Logf("path failure callback: %s", reason)
		// Production FailReadyForExit closes the shared session the same way.
		_ = direct.Close()
	})
	existing, err := d.DialTCP(ctx, "exit", "fast.example", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	replacement, err := d.DialTCP(ctx, "exit", "slow.example", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if invalidated.Load() || relay.opens.Load() != 1 {
		t.Fatalf("healthy session invalidated=%t, relay opens=%d", invalidated.Load(), relay.opens.Load())
	}
	peer := <-existingPeer
	sent := make(chan error, 1)
	go func() { _, err := peer.Write([]byte("ok")); sent <- err }()
	_ = existing.SetReadDeadline(time.Now().Add(time.Second))
	payload := make([]byte, 2)
	if _, err := io.ReadFull(existing, payload); err != nil || string(payload) != "ok" {
		t.Fatalf("sibling stream interrupted: %q %v", payload, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
