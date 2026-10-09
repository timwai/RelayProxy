package bridge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/agent/exit"
)

func TestCustomExitUDPProbeExchangesDNSDatagrams(t *testing.T) {
	runCustomExitUDPProbe(t, false)
}

func TestCustomExitUDPProbeRejectsMismatchedDNSResponse(t *testing.T) {
	runCustomExitUDPProbe(t, true)
}

func runCustomExitUDPProbe(t *testing.T, badResponse bool) {
	t.Helper()
	relay, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	_ = relay.SetDeadline(time.Now().Add(3 * time.Second))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverErrors := make(chan error, 2)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		var greeting [3]byte
		if _, err := io.ReadFull(conn, greeting[:]); err != nil {
			serverErrors <- err
			return
		}
		if greeting != [3]byte{5, 1, 0} {
			serverErrors <- fmt.Errorf("unexpected SOCKS5 auth offer %x", greeting)
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			serverErrors <- err
			return
		}
		var associate [10]byte
		if _, err := io.ReadFull(conn, associate[:]); err != nil {
			serverErrors <- err
			return
		}
		if associate != [10]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0} {
			serverErrors <- fmt.Errorf("unexpected UDP ASSOCIATE %x", associate)
			return
		}
		address := relay.LocalAddr().(*net.UDPAddr)
		ip := address.IP.To4()
		reply := append([]byte{5, 0, 0, 1}, ip...)
		reply = binary.BigEndian.AppendUint16(reply, uint16(address.Port))
		if _, err := conn.Write(reply); err != nil {
			serverErrors <- err
			return
		}
		_, err = io.Copy(io.Discard, conn)
		serverErrors <- err
	}()
	go func() {
		buf := make([]byte, 4096)
		n, addr, err := relay.ReadFrom(buf)
		if err != nil {
			serverErrors <- err
			return
		}
		header := []byte{0, 0, 0, 1, 9, 9, 9, 9, 0, 53}
		if n <= len(header) || !bytes.Equal(buf[:len(header)], header) {
			serverErrors <- fmt.Errorf("unexpected remote UDP destination header %x", buf[:n])
			return
		}
		var query dnsmessage.Message
		if err := query.Unpack(buf[len(header):n]); err != nil {
			serverErrors <- err
			return
		}
		if len(query.Questions) != 1 || query.Questions[0].Name.String() != "example.com." {
			serverErrors <- fmt.Errorf("unexpected outbound query %+v", query.Questions)
			return
		}
		response := dnsmessage.Message{
			Header: dnsmessage.Header{ID: query.ID, Response: true, RecursionDesired: true, RecursionAvailable: true},
			Questions: query.Questions,
		}
		if badResponse {
			response.ID++
		}
		payload, err := response.Pack()
		if err != nil {
			serverErrors <- err
			return
		}
		packet := append(append([]byte(nil), header...), payload...)
		_, err = relay.WriteTo(packet, addr)
		serverErrors <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	duration, err := probeCustomExitUDP(ctx, exit.UpstreamConfig{
		Mode: exit.UpstreamSOCKS5, Address: listener.Addr().String(),
	})
	if badResponse {
		if err == nil || !strings.Contains(err.Error(), "invalid response") {
			t.Fatalf("accepted mismatched UDP DNS answer: duration=%v err=%v", duration, err)
		}
	} else if err != nil || duration < 0 {
		t.Fatalf("UDP relay could not exchange DNS response: duration=%v err=%v", duration, err)
	}
	for i := 0; i < 2; i++ {
		select {
		case serverErr := <-serverErrors:
			if serverErr != nil && !errors.Is(serverErr, net.ErrClosed) {
				t.Fatalf("fake SOCKS5 proxy error: %v", serverErr)
			}
		case <-ctx.Done():
			t.Fatal("fake SOCKS5 proxy did not complete")
		}
	}
}
