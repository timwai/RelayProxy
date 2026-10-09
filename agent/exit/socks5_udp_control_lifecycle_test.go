package exit

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

// A SOCKS5 UDP association must expire when the proxy closes its TCP control
// channel. Otherwise a transparent UDP flow may appear active indefinitely
// after the upstream proxy has restarted.
func TestCustomSOCKS5UDPControlCloseUnblocksRead(t *testing.T) {
	relay, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	dropControl := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if err := acceptSOCKS5NoAuth(conn); err != nil {
			serverDone <- err
			return
		}
		var header [4]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			serverDone <- err
			return
		}
		if header != [4]byte{5, 3, 0, 1} {
			serverDone <- errors.New("expected SOCKS5 UDP ASSOCIATE")
			return
		}
		if _, err := readSOCKS5Address(conn, header[3]); err != nil {
			serverDone <- err
			return
		}
		bound := relay.LocalAddr().(*net.UDPAddr).AddrPort()
		addr, err := encodeSOCKS5Address(bound.Addr().String(), bound.Port())
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := conn.Write(append([]byte{5, 0, 0}, addr...)); err != nil {
			serverDone <- err
			return
		}
		<-dropControl
		serverDone <- nil // closing conn in defer simulates a dropped proxy
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pc, err := DialViaUpstreamUDP(ctx, UpstreamConfig{
		Mode: UpstreamSOCKS5, Address: proxy.Addr().String(),
	}, "example.invalid", 443)
	if err != nil {
		close(dropControl)
		t.Fatal(err)
	}
	defer pc.Close()
	if err := pc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		close(dropControl)
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 256)
		_, _, err := pc.ReadFrom(buffer)
		readDone <- err
	}()
	close(dropControl)
	select {
	case err := <-readDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("UDP read after TCP control shutdown = %v, want closed socket", err)
		}
	case <-ctx.Done():
		t.Fatal("UDP read did not unblock after SOCKS5 control closed")
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS5 server did not exit")
	}
}
