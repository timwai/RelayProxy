package rdp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type controllerPacketConn struct {
	writes    chan []byte
	readError chan error
	writeErr  error
	done      chan struct{}
	closeOnce sync.Once
}

func newControllerPacketConn() *controllerPacketConn {
	return &controllerPacketConn{writes: make(chan []byte, 4), readError: make(chan error, 1), done: make(chan struct{})}
}

func (c *controllerPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	select {
	case err := <-c.readError:
		return 0, nil, err
	case <-c.done:
		return 0, nil, net.ErrClosed
	}
}

func (c *controllerPacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	packet := append([]byte(nil), payload...)
	select {
	case c.writes <- packet:
		return len(payload), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *controllerPacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}
func (c *controllerPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *controllerPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *controllerPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *controllerPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestStartControllerPipesTCPAndDisablesUDPWithoutDatagrams(t *testing.T) {
	targetID := "dev_target"
	seenTarget := make(chan string, 1)
	serverReceived := make(chan string, 1)
	connection, err := StartController(context.Background(), Target{DeviceID: targetID, Name: "Target", Online: true}, ControllerOptions{
		DialTCP: func(_ context.Context, id string) (net.Conn, error) {
			seenTarget <- id
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				buffer := make([]byte, 32)
				n, err := server.Read(buffer)
				if err == nil {
					serverReceived <- string(buffer[:n])
					_, _ = server.Write([]byte("rdp-ok"))
				}
			}()
			return client, nil
		},
		SupportsUDP: func() bool { return false },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.UDPEnabled || connection.UDPConn != nil {
		t.Fatalf("UDP should be disabled on a non-datagram transport: %+v", connection)
	}
	if connection.ListenAddr[:len("127.0.0.1:")] != "127.0.0.1:" {
		t.Fatalf("controller must bind loopback, got %q", connection.ListenAddr)
	}

	local, err := net.DialTimeout("tcp", connection.ListenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	_ = local.SetDeadline(time.Now().Add(time.Second))
	if _, err := local.Write([]byte("rdp-client")); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(io.LimitReader(local, int64(len("rdp-ok"))))
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "rdp-ok" {
		t.Fatalf("unexpected RDP response %q", response)
	}
	select {
	case id := <-seenTarget:
		if id != targetID {
			t.Fatalf("dialed target %q, want %q", id, targetID)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not dial target")
	}
	select {
	case payload := <-serverReceived:
		if payload != "rdp-client" {
			t.Fatalf("target received %q", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("target did not receive controller payload")
	}
}

func TestStartControllerEnablesUDPAndReportsAssociationState(t *testing.T) {
	connection, err := StartController(context.Background(), Target{DeviceID: "udp-target"}, ControllerOptions{
		DialTCP: func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("TCP is not used by this test")
		},
		DialUDP: func(context.Context, string) (net.PacketConn, error) {
			return nil, errors.New("UDP relay unavailable")
		},
		SupportsUDP: func() bool { return true },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	enabled, active, lastError := connection.UDPStatus()
	if !enabled || active || lastError != "" || connection.UDPConn == nil {
		t.Fatalf("unexpected initial UDP status: enabled=%v active=%v error=%q conn=%v", enabled, active, lastError, connection.UDPConn)
	}
	if got := connection.UDPConn.LocalAddr().(*net.UDPAddr).Port; got != connection.TCPListener.Addr().(*net.TCPAddr).Port {
		t.Fatalf("TCP/UDP loopback ports differ: tcp=%d udp=%d", connection.TCPListener.Addr().(*net.TCPAddr).Port, got)
	}

	client, err := net.DialUDP("udp", nil, connection.UDPConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, active, lastError = connection.UDPStatus()
		if lastError != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if active || lastError != "UDP relay unavailable" {
		t.Fatalf("UDP association failure was not surfaced: active=%v error=%q", active, lastError)
	}
}

func TestControllerRetriesFirstPacketAfterStaleUDPWrite(t *testing.T) {
	stale := newControllerPacketConn()
	stale.writeErr = net.ErrClosed
	healthy := newControllerPacketConn()
	var calls atomic.Int32
	connection, err := StartController(context.Background(), Target{DeviceID: "udp-target"}, ControllerOptions{
		DialTCP: func(context.Context, string) (net.Conn, error) { return nil, errors.New("unused") },
		DialUDP: func(context.Context, string) (net.PacketConn, error) {
			if calls.Add(1) == 1 {
				return stale, nil
			}
			return healthy, nil
		},
		SupportsUDP: func() bool { return true },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client, err := net.DialUDP("udp", nil, connection.UDPConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("must-not-drop")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-healthy.writes:
		if string(got) != "must-not-drop" || calls.Load() != 2 {
			t.Fatalf("retry got=%q dialCalls=%d", got, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("replacement association did not receive the first packet")
	}
}

func TestControllerReadFailureDetachesUDPAssociation(t *testing.T) {
	first, second := newControllerPacketConn(), newControllerPacketConn()
	var calls atomic.Int32
	connection, err := StartController(context.Background(), Target{DeviceID: "udp-target"}, ControllerOptions{
		DialTCP: func(context.Context, string) (net.Conn, error) { return nil, errors.New("unused") },
		DialUDP: func(context.Context, string) (net.PacketConn, error) {
			if calls.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
		SupportsUDP: func() bool { return true },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client, err := net.DialUDP("udp", nil, connection.UDPConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.writes:
	case <-time.After(time.Second):
		t.Fatal("initial association did not receive packet")
	}
	first.readError <- errors.New("association expired")
	deadline := time.Now().Add(time.Second)
	for {
		_, active, lastError := connection.UDPStatus()
		if !active && lastError == "association expired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read failure did not update state: active=%v error=%q", active, lastError)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := client.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-second.writes:
		if string(got) != "second" || calls.Load() != 2 {
			t.Fatalf("replacement got=%q dialCalls=%d", got, calls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("read-side failure left the stale association installed")
	}
}
