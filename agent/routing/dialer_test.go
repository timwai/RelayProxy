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
	legacy, required, preferred int
	exit                        string
	err                         error
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
	if opts.PreferStream {
		d.preferred++
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

func TestRoutingPropagatesUDPStreamPreference(t *testing.T) {
	engine, err := NewEngine(Config{
		Mode:  ModeRule,
		Rules: []Rule{{Enabled: true, Action: ActionProxy, ExitID: "chosen-exit"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := errors.New("preferred stream probe")
	underlying := &requiredDialer{err: expected}
	dialer := NewRoutingDialer(engine, underlying)

	_, err = dialer.DialUDPWithOptions(
		context.Background(),
		"other-exit",
		"youtube.googleapis.com",
		443,
		proxy.UDPDialOptions{PreferStream: true},
	)
	if !errors.Is(err, expected) || underlying.preferred != 1 || underlying.required != 0 ||
		underlying.legacy != 0 || underlying.exit != "chosen-exit" {
		t.Fatalf("stream preference lost: err=%v underlying=%+v", err, underlying)
	}
}

func TestRoutingNativeRequirementOverridesStreamPreference(t *testing.T) {
	engine, err := NewEngine(Config{
		Mode:  ModeRule,
		Rules: []Rule{{Enabled: true, Action: ActionProxy, DatagramRequired: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := errors.New("required datagram probe")
	underlying := &requiredDialer{err: expected}
	dialer := NewRoutingDialer(engine, underlying)

	_, err = dialer.DialUDPWithOptions(
		context.Background(),
		"exit",
		"realtime.example",
		443,
		proxy.UDPDialOptions{PreferStream: true},
	)
	if !errors.Is(err, expected) || underlying.required != 1 || underlying.preferred != 0 || underlying.legacy != 0 {
		t.Fatalf("required datagram did not override stream preference: err=%v underlying=%+v", err, underlying)
	}
}


func TestRoutingProxyPauseRejectsTCPAndUDPProxyDials(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeProxy})
	if err != nil {
		t.Fatal(err)
	}
	underlying := &requiredDialer{err: errors.New("underlying dialer must not be called")}
	dialer := NewRoutingDialer(engine, underlying)
	dialer.ProxyPaused = func() bool { return true }

	if _, err := dialer.dialTCP(context.Background(), "exit", "example.com", 443, Decision{Action: ActionProxy}); !errors.Is(err, ErrProxyPaused) {
		t.Fatalf("paused TCP proxy dial error = %v, want %v", err, ErrProxyPaused)
	}
	if _, err := dialer.dialUDP(context.Background(), "exit", "example.com", 443, Decision{Action: ActionProxy}, proxy.UDPDialOptions{}); !errors.Is(err, ErrProxyPaused) {
		t.Fatalf("paused UDP proxy dial error = %v, want %v", err, ErrProxyPaused)
	}
	if underlying.legacy != 0 || underlying.required != 0 || underlying.preferred != 0 {
		t.Fatalf("paused proxy reached underlying dialer: %+v", underlying)
	}
}
