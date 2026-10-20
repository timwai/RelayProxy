package rdp

import (
	"context"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestControllerQuietTCPRemainsUsableUntilSessionEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		local, client := net.Pipe()
		defer client.Close()
		remote, target := net.Pipe()
		defer target.Close()
		c := &Connection{Target: Target{DeviceID: "target"}}
		done := make(chan struct{})
		go func() {
			c.handleTCP(ctx, local, func(context.Context, string) (net.Conn, error) { return remote, nil })
			close(done)
		}()
		synctest.Wait()
		// The main TCP connection may be quiet while UDP carries the desktop.
		time.Sleep(31 * time.Minute)
		for _, pair := range [][2]net.Conn{{client, target}, {target, client}} {
			written := make(chan error, 1)
			go func() { _, err := pair[0].Write([]byte("rdp")); written <- err }()
			var payload [3]byte
			if _, err := io.ReadFull(pair[1], payload[:]); err != nil {
				t.Fatalf("quiet RDP TCP connection was closed: %v", err)
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
			t.Fatal("session cancellation did not release the quiet TCP connection")
		}
	})
}
