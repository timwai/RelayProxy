package exit

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

type pipeTunnelStream struct {
	net.Conn
}

func (p pipeTunnelStream) CloseWrite() error { return nil }

func TestHandleOpenUDPEcho(t *testing.T) {
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()

	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buf[:n], addr)
		}
	}()

	host, portStr, err := net.SplitHostPort(echo.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	clientEnd, exitEnd := net.Pipe()
	defer clientEnd.Close()

	h := NewHandler(HandlerConfig{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.HandleStream(context.Background(), pipeTunnelStream{Conn: exitEnd})
	}()

	header := &protocol.StreamHeader{
		Magic:     protocol.MagicHeader,
		Version:   protocol.CurrentVersion,
		Type:      protocol.FrameTypeOpenUDP,
		RequestID: "req_udp1",
	}
	if err := protocol.WriteStreamHeader(clientEnd, header); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(clientEnd, protocol.OpenUDPRequest{
		RequestID: "req_udp1",
		Host:      host,
		Port:      uint16(port),
		TimeoutMs: 5000,
	}); err != nil {
		t.Fatal(err)
	}

	_ = clientEnd.SetDeadline(time.Now().Add(5 * time.Second))
	var resp protocol.OpenUDPResponse
	if err := protocol.ReadJSON(clientEnd, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("OpenUDP failed: %s %s", resp.ErrorCode, resp.ErrorMessage)
	}

	if err := protocol.WriteUDPDatagram(clientEnd, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := protocol.ReadUDPDatagram(clientEnd)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ping" {
		t.Fatalf("got %q want ping", got)
	}

	_ = clientEnd.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("HandleStream did not exit")
	}
}
