package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestAddressRaceEscapesBlackholeAndCancelsLoser(t *testing.T) {
	client, _ := sessionPair(t, "quic")
	blockedDone := make(chan struct{})
	start := time.Now()
	s, err := raceQUICAddresses(context.Background(), []net.UDPAddr{{Port: 1}, {Port: 2}}, 20*time.Millisecond, func(ctx context.Context, addr net.UDPAddr) (*QUICSession, error) {
		if addr.Port == 1 {
			<-ctx.Done()
			close(blockedDone)
			return nil, ctx.Err()
		}
		return client.(*QUICSession), nil
	})
	if err != nil || s != client || time.Since(start) > time.Second {
		t.Fatalf("race stalled: %v", err)
	}
	select {
	case <-blockedDone:
	case <-time.After(time.Second):
		t.Fatal("loser not canceled")
	}
}

func TestAddressRaceClosesLateSuccessfulSocket(t *testing.T) {
	winner, _ := sessionPair(t, "quic")
	loser, _ := sessionPair(t, "quic")
	started := make(chan struct{})
	s, err := raceQUICAddresses(context.Background(), []net.UDPAddr{{Port: 1}, {Port: 2}}, time.Millisecond, func(ctx context.Context, addr net.UDPAddr) (*QUICSession, error) {
		if addr.Port == 1 {
			<-started
			return winner.(*QUICSession), nil
		}
		close(started)
		<-ctx.Done()
		return loser.(*QUICSession), nil
	})
	if err != nil || s != winner {
		t.Fatalf("winner=%p err=%v", s, err)
	}
	select {
	case <-loser.Done():
	case <-time.After(time.Second):
		t.Fatal("late success leaked")
	}
	// Close waits for both QUIC shutdown and release of the owned UDP socket.
	_ = loser.Close()
	pc := loser.(*QUICSession).ownedPacketConn
	if _, err := pc.WriteTo([]byte{1}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("socket leaked: %v", err)
	}
}

func TestQUICHostnameRacePreservesSNI(t *testing.T) {
	sni := make(chan string, 1)
	l, err := ListenDirectQUIC("127.0.0.1:0", &tls.Config{
		Certificates:       []tls.Certificate{generateSelfSignedCert(t)},
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) { sni <- h.ServerName; return nil, nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := DialDirectQUIC(ctx, net.JoinHostPort("localhost", strconv.Itoa(l.Addr().(*net.UDPAddr).Port)), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case name := <-sni:
		if name != "localhost" {
			t.Fatalf("SNI=%q", name)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if s.dialTimings == nil || s.dialTimings.AddressCount == 0 {
		t.Fatal("missing DNS/handshake timings")
	}
}

func TestQUICResolutionCancellationAndZone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolveQUICAddresses(ctx, "relay.invalid:443"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DNS cancellation: %v", err)
	}
	addresses, _, err := resolveQUICAddresses(context.Background(), "[fe80::1%en0]:443")
	if err != nil || len(addresses) != 1 || addresses[0].Zone != "en0" {
		t.Fatalf("IPv6 zone lost: %v %v", addresses, err)
	}
}
