package portforward

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

type echoTransport struct{}

func (echoTransport) DialTCP(ctx context.Context, mapping Mapping) (net.Conn, Path, error) {
	address := net.JoinHostPort(mapping.TargetAddress, strconv.Itoa(int(mapping.TargetPort)))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	return conn, PathRelayTLS, err
}

func (echoTransport) OpenUDP(ctx context.Context, mapping Mapping) (DatagramSession, Path, error) {
	address := net.JoinHostPort(mapping.TargetAddress, strconv.Itoa(int(mapping.TargetPort)))
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", address)
	if err != nil {
		return nil, "", err
	}
	return &testDatagramSession{Conn: conn}, PathRelayQUIC, nil
}

type testDatagramSession struct {
	net.Conn
}

func (s *testDatagramSession) Send(ctx context.Context, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.Write(payload)
	return err
}

func (s *testDatagramSession) Receive(ctx context.Context) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	buffer := make([]byte, maxUDPPayload)
	n, err := s.Read(buffer)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return append([]byte(nil), buffer[:n]...), nil
}

func (s *testDatagramSession) Close() error {
	return s.Conn.Close()
}

func startEchoTargets(t *testing.T) uint16 {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	udp, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		_ = tcp.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = tcp.Close()
		_ = udp.Close()
	})
	go func() {
		for {
			conn, acceptErr := tcp.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	go func() {
		buffer := make([]byte, maxUDPPayload)
		for {
			n, addr, readErr := udp.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			_, _ = udp.WriteTo(buffer[:n], addr)
		}
	}()
	return uint16(port)
}

func TestManagerForwardsTCPAndUDPOnOneLocalPort(t *testing.T) {
	targetPort := startEchoTargets(t)
	manager := NewManager(context.Background(), echoTransport{}, Options{})
	defer manager.Close()

	err := manager.Add(Mapping{
		ID: "web-and-datagram", Name: "Echo service", RemoteDeviceID: "target-device",
		Protocol: ProtocolBoth, TargetAddress: "127.0.0.1", TargetPort: targetPort, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := manager.Get("web-and-datagram")
	if !ok {
		t.Fatal("mapping was not registered")
	}
	if snapshot.State != StateListening || snapshot.ListenPort == 0 {
		t.Fatalf("mapping did not start on a stable port: %+v", snapshot)
	}

	tcpConn, err := net.DialTimeout("tcp", snapshot.ListenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tcpPayload := []byte("relayproxy-tcp")
	if _, err := tcpConn.Write(tcpPayload); err != nil {
		tcpConn.Close()
		t.Fatal(err)
	}
	_ = tcpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	tcpReply := make([]byte, len(tcpPayload))
	if _, err := io.ReadFull(tcpConn, tcpReply); err != nil {
		tcpConn.Close()
		t.Fatal(err)
	}
	_ = tcpConn.Close()
	if string(tcpReply) != string(tcpPayload) {
		t.Fatalf("TCP echo mismatch: got %q want %q", tcpReply, tcpPayload)
	}

	udpConn, err := net.DialTimeout("udp", snapshot.ListenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	_ = udpConn.SetDeadline(time.Now().Add(2 * time.Second))
	udpPayload := []byte("relayproxy-udp")
	if _, err := udpConn.Write(udpPayload); err != nil {
		t.Fatal(err)
	}
	udpReply := make([]byte, len(udpPayload))
	if _, err := io.ReadFull(udpConn, udpReply); err != nil {
		t.Fatal(err)
	}
	if string(udpReply) != string(udpPayload) {
		t.Fatalf("UDP echo mismatch: got %q want %q", udpReply, udpPayload)
	}
}

func TestMappingRejectsWildcardListenerByDefault(t *testing.T) {
	_, err := normalizeMapping(Mapping{
		ID: "unsafe", RemoteDeviceID: "target-device", Protocol: ProtocolTCP,
		ListenAddress: "0.0.0.0", TargetAddress: "127.0.0.1", TargetPort: 22,
	})
	if err == nil {
		t.Fatal("expected wildcard listener to be rejected")
	}
}

func TestManagerStartStopAndRemove(t *testing.T) {
	targetPort := startEchoTargets(t)
	manager := NewManager(context.Background(), echoTransport{}, Options{})
	defer manager.Close()

	mapping := Mapping{
		ID: "toggle", RemoteDeviceID: "target-device", Protocol: ProtocolTCP,
		TargetAddress: "127.0.0.1", TargetPort: targetPort,
	}
	if err := manager.Add(mapping); err != nil {
		t.Fatal(err)
	}
	if err := manager.StartMapping("toggle"); err != nil {
		t.Fatal(err)
	}
	first, _ := manager.Get("toggle")
	if first.State != StateListening {
		t.Fatalf("expected listening state, got %s", first.State)
	}
	if err := manager.StopMapping("toggle"); err != nil {
		t.Fatal(err)
	}
	stopped, _ := manager.Get("toggle")
	if stopped.State != StateStopped {
		t.Fatalf("expected stopped state, got %s", stopped.State)
	}
	if err := manager.StartMapping("toggle"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove("toggle"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("toggle"); ok {
		t.Fatal("mapping remained after remove")
	}
}
