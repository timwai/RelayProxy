package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/speedtest"
	"relayproxy/internal/tunnel"
)

// A real half-close is essential: a no-op CloseWrite would never finish the
// zero-payload upload and could conceal a leaked server-side speed test.
func exitProbeSession(t *testing.T, serve func(*net.TCPConn) error) (*scriptedSession, <-chan error) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err == nil {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			err = serve(conn)
		}
		done <- err
	}()
	session := newScriptedSession(func() tunnel.TunnelStream {
		conn, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
		if err != nil {
			t.Fatal(err)
		}
		return conn
	})
	return session, done
}

func TestProbeExitUsesRequestedExitAndZeroPayloadHandshake(t *testing.T) {
	for _, path := range []protocol.ProxyPath{protocol.ProxyPathRelayTLS, protocol.ProxyPathP2PQUIC, protocol.ProxyPathPublicDirectQUIC} {
		t.Run(path.String(), func(t *testing.T) {
			sess, served := exitProbeSession(t, func(stream *net.TCPConn) error {
				header, err := protocol.ReadStreamHeader(stream)
				if err != nil {
					return err
				}
				var req protocol.SpeedTestRequest
				if err := protocol.ReadJSON(stream, &req); err != nil {
					return err
				}
				if header.Type != protocol.FrameTypeSpeedTest || header.ExitDeviceID != "chosen" || header.ClientDeviceID != "android" || req.RequestID != header.RequestID || req.Direction != protocol.SpeedTestUpload {
					return fmt.Errorf("unexpected probe: %+v / %+v", header, req)
				}
				measurement, err := speedtest.Serve(stream, req)
				if measurement.Bytes != 0 {
					return fmt.Errorf("latency probe uploaded %d bytes", measurement.Bytes)
				}
				return err
			})
			d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, func() string { return "android" })
			// A different configured default must never change the requested probe.
			d.SetDefaultExitID("other")
			if path != protocol.ProxyPathRelayTLS {
				d.ConfigurePathProvider(func(id string) (SelectedSession, bool) {
					return SelectedSession{Session: sess, Path: path}, id == "chosen"
				}, nil)
			}
			result, err := d.ProbeExit(context.Background(), "chosen")
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitID != "chosen" || result.Path != path.String() || result.LatencyMS <= 0 {
				t.Fatalf("invalid result: %+v", result)
			}
			if err := <-served; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProbeExitRejectsFailedAndMismatchedResponses(t *testing.T) {
	for name, ack := range map[string]protocol.SpeedTestResponse{
		"offline":    {RequestID: "probe_1", ErrorCode: protocol.ErrCodeExitOffline, ErrorMessage: "offline"},
		"mismatched": {RequestID: "another-request", Success: true},
	} {
		t.Run(name, func(t *testing.T) {
			stream := responseStream(ack)
			sess := newScriptedSession(func() tunnel.TunnelStream { return stream })
			d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
			if result, err := d.ProbeExit(context.Background(), "exit"); err == nil || result.LatencyMS != 0 {
				t.Fatalf("unexpected success: %+v / %v", result, err)
			}
			if !stream.closed.Load() {
				t.Fatal("failed probe leaked a stream")
			}
		})
	}
}

func TestProbeExitCancellationClosesBlockedHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess, served := exitProbeSession(t, func(stream *net.TCPConn) error {
		if _, err := protocol.ReadStreamHeader(stream); err != nil {
			return err
		}
		var req protocol.SpeedTestRequest
		if err := protocol.ReadJSON(stream, &req); err != nil {
			return err
		}
		cancel() // No response: cancellation must interrupt the blocked read.
		_, err := io.Copy(io.Discard, stream)
		return err
	})
	d := NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
	if result, err := d.ProbeExit(ctx, "exit"); err == nil || result.LatencyMS != 0 {
		t.Fatalf("canceled probe reported success: %+v / %v", result, err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}
