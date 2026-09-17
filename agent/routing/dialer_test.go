package routing

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/proxy"
)

func TestDirectUDPUsesFixedConnectedTarget(t *testing.T) {
	target, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	engine, err := NewEngine(Config{Mode: ModeDirect})
	if err != nil {
		t.Fatal(err)
	}
	dialer := NewRoutingDialer(engine, nil)
	conn, err := dialer.DialUDP(context.Background(), "", "127.0.0.1", uint16(target.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_ = target.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.WriteTo([]byte("request"), target.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	n, source, err := target.ReadFromUDP(buffer)
	if err != nil || string(buffer[:n]) != "request" {
		t.Fatalf("target read: %q %v", buffer[:n], err)
	}
	if _, err := target.WriteToUDP([]byte("response"), source); err != nil {
		t.Fatal(err)
	}
	n, _, err = conn.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != "response" {
		t.Fatalf("direct reply: %q %v", buffer[:n], err)
	}
	wrong := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: target.LocalAddr().(*net.UDPAddr).Port + 1}
	if _, err := conn.WriteTo([]byte("wrong"), wrong); err == nil {
		t.Fatal("association sent to an unbound target")
	}
}

type requiredDialer struct {
	legacy, required int
	exit             string
	err              error
}

func (d *requiredDialer) DialTCP(context.Context, string, string, uint16) (net.Conn, error) {
	return nil, d.err
}
func (d *requiredDialer) DialUDP(context.Context, string, string, uint16) (net.PacketConn, error) {
	d.legacy++
	return nil, d.err
}
func (d *requiredDialer) DialUDPWithOptions(_ context.Context, exit, host string, port uint16, opts proxy.UDPDialOptions) (net.PacketConn, error) {
	if opts.DatagramRequired {
		d.required++
	}
	d.exit = exit
	return nil, d.err
}

func TestRoutingPropagatesNativeRequirementWithoutFallback(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeRule, Rules: []Rule{{Enabled: true, Action: ActionProxy, ExitID: "chosen-exit", DatagramRequired: true}}})
	if err != nil {
		t.Fatal(err)
	}
	expected := errors.New("native UDP unavailable")
	underlying := &requiredDialer{err: expected}
	dialer := NewRoutingDialer(engine, underlying)
	_, err = dialer.DialUDP(context.Background(), "other-exit", "example.com", 443)
	if !errors.Is(err, expected) || underlying.required != 1 || underlying.legacy != 0 || underlying.exit != "chosen-exit" {
		t.Fatalf("required policy lost: err=%v underlying=%+v", err, underlying)
	}
}
