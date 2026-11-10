package app

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/punch"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestPunchedFileCloseReleasesUDPSocket(t *testing.T) {
	client, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	host, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	cc := candidate.Discover(client.LocalAddr().(*net.UDPAddr).Port, 0)
	hc := candidate.Discover(host.LocalAddr().(*net.UDPAddr).Port, 0)
	if len(cc) == 0 || len(hc) == 0 {
		t.Skip("no LAN interface")
	}
	cfg, fp, err := makeFileDirectTLS()
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hostDone := make(chan error, 1)
	go func() {
		result, err := punch.Punch(ctx, host, cc, 42, key, time.Second)
		if err != nil {
			hostDone <- err
			return
		}
		tr := &quic.Transport{Conn: result.Conn}
		defer tr.Close()
		listener, err := tr.Listen(fileDirectQUICServerTLS(cfg), fileDirectQUICConfig())
		if err != nil {
			hostDone <- err
			return
		}
		defer listener.Close()
		conn, err := listener.Accept(ctx)
		if err != nil {
			hostDone <- err
			return
		}
		defer conn.CloseWithError(0, "")
		session := tunnel.NewQUICSession(conn)
		stream, err := session.AcceptStream(ctx)
		if err != nil {
			hostDone <- err
			return
		}
		defer stream.Close()
		stream.SetDeadline(time.Now().Add(3 * time.Second))
		var auth protocol.FileDirectAuth
		if err = protocol.ReadJSON(stream, &auth); err != nil {
			hostDone <- err
			return
		}
		if err = protocol.WriteJSON(stream, protocol.FileDirectAuthResult{Allowed: true}); err != nil {
			hostDone <- err
			return
		}
		var request protocol.FileShareRequest
		if err = protocol.ReadJSON(stream, &request); err != nil {
			hostDone <- err
			return
		}
		if err = protocol.WriteJSON(stream, protocol.FileShareResponse{Success: true}); err != nil {
			hostDone <- err
			return
		}
		<-conn.Context().Done()
		hostDone <- nil
	}()
	stream, _, err := (&Agent{}).tryPunchedFileQUIC(ctx, client, protocol.FileDirectOffer{Fingerprint: fp, Token: "test-ticket", PunchSessionID: 42, PunchKey: key, PunchCandidates: hc}, protocol.FileShareRequest{Action: protocol.FileShareListShares})
	if err != nil {
		t.Fatal(err)
	}
	info := stream.(protocol.ConnectionPathProvider).ConnectionPath()
	if info.Path != "p2p_quic" || info.Transport != "quic" || info.RemoteAddress == "" || info.CandidateType == "" {
		t.Fatalf("missing selected file path: %+v", info)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err = client.SetReadDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Errorf("stream.Close left original UDP socket open: SetReadDeadline=%v", err)
	}
	if err = <-hostDone; err != nil {
		t.Fatal(err)
	}
}
