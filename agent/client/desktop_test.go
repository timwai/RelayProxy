package client

import (
	"bytes"
	"context"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestDialDesktopMediaNegotiatesNativeDatagrams(t *testing.T) {
	clientSession, relaySession := dialerQUICPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	served := make(chan error, 1)
	relayChannel := make(chan *tunnel.DatagramChannel, 1)
	release := make(chan struct{})
	go func() {
		stream, err := relaySession.AcceptStream(ctx)
		if err != nil {
			served <- err
			return
		}
		defer stream.Close()
		header, err := protocol.ReadStreamHeader(stream)
		if err != nil {
			served <- err
			return
		}
		if header.Type != protocol.FrameTypeOpenDesktopMedia || header.ExitDeviceID != "desktop-target" {
			served <- protocol.NewRelayError(protocol.ErrCodeInvalidRequest, "unexpected desktop media header")
			return
		}
		var req protocol.OpenDesktopMediaRequest
		if err := protocol.ReadJSON(stream, &req); err != nil {
			served <- err
			return
		}
		if req.Mode != protocol.DesktopMediaModeDatagram || req.AssociationID == 0 {
			served <- protocol.NewRelayError(protocol.ErrCodeInvalidRequest, "invalid desktop media request")
			return
		}
		channel, err := tunnel.OpenDesktopDatagramChannel(relaySession, req.AssociationID)
		if err != nil {
			served <- err
			return
		}
		defer channel.Close()
		if err := protocol.WriteJSON(stream, protocol.OpenDesktopMediaResponse{
			RequestID: req.RequestID, Success: true, Mode: protocol.DesktopMediaModeDatagram, AssociationID: req.AssociationID,
		}); err != nil {
			served <- err
			return
		}
		relayChannel <- channel
		<-release
		served <- nil
	}()

	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return clientSession }, func() string { return "controller" })
	conn, err := dialer.DialDesktopMedia(ctx, "desktop-target")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer conn.Close()
	channel := <-relayChannel

	outbound := []byte("encoded-h264-access-unit")
	if err := conn.Send(ctx, outbound); err != nil {
		close(release)
		t.Fatal(err)
	}
	got, err := channel.Receive(ctx)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if !bytes.Equal(got, outbound) {
		close(release)
		t.Fatalf("relay got %q", got)
	}

	inbound := []byte("media-feedback")
	if err := channel.Send(ctx, inbound); err != nil {
		close(release)
		t.Fatal(err)
	}
	got, err = conn.Receive(ctx)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if !bytes.Equal(got, inbound) {
		close(release)
		t.Fatalf("client got %q", got)
	}
	close(release)
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestDialDesktopMediaRejectsNonDatagramTransport(t *testing.T) {
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return streamOnlySession{} }, nil)
	conn, err := dialer.DialDesktopMedia(context.Background(), "target")
	if conn != nil || err == nil {
		t.Fatalf("conn=%v err=%v", conn, err)
	}
}
