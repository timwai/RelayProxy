package divert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
)

type testUDPOptionsDialer struct {
	*testDialer
	udpWithOptions func(context.Context, string, string, uint16, proxy.UDPDialOptions) (net.PacketConn, error)
}

func (d *testUDPOptionsDialer) DialUDPWithOptions(ctx context.Context, exit, host string, port uint16, opts proxy.UDPDialOptions) (net.PacketConn, error) {
	return d.udpWithOptions(ctx, exit, host, port, opts)
}

func TestUDPDatagramRequiredUsesFrozenRuleAndReusesAssociation(t *testing.T) {
	target, observed := udpReplyServer(t, 0)
	var preferredCalls, requiredCalls atomic.Int32
	requiredDialer := connectedUDPDialer(&requiredCalls)
	dialer := &testUDPOptionsDialer{
		testDialer: connectedUDPDialer(&preferredCalls),
		udpWithOptions: func(ctx context.Context, exit, host string, port uint16, opts proxy.UDPDialOptions) (net.PacketConn, error) {
			if !opts.DatagramRequired || exit != "exit-required" || host != target.IP.String() || port != uint16(target.Port) {
				return nil, fmt.Errorf("lost frozen UDP decision: exit=%s host=%s port=%d options=%+v", exit, host, port, opts)
			}
			return requiredDialer.DialUDP(ctx, exit, host, port)
		},
	}
	cfg := Config{DefaultAction: ActionReject, Rules: []Rule{{
		Name: "browser", Enabled: true, Process: "browser.exe", Action: ActionProxy,
		ExitID: "exit-required", DatagramRequired: true,
	}}}
	server := newTestServer(t, Options{Dialer: dialer, Config: cfg})
	flow := testFlow(ProtoUDP, target)
	route, err := server.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if !route.Decision().DatagramRequired {
		t.Fatal("classification dropped the required datagram setting")
	}

	// Change policy before the first packet is forwarded. The old association
	// retains its decision; only a new five-tuple uses the preferred fallback.
	cfg.Rules[0].DatagramRequired = false
	cfg.Rules[0].ExitID = "exit-preferred"
	if err := server.ReloadRules(cfg); err != nil {
		t.Fatal(err)
	}
	same, err := server.ClassifyFlow(flow)
	if err != nil || same != route || !same.Decision().DatagramRequired {
		t.Fatalf("reload changed an existing UDP decision: route=%+v err=%v", same, err)
	}
	flow.SourcePort++
	preferred, err := server.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if preferred.Decision().DatagramRequired {
		t.Fatal("new flow ignored the preferred datagram policy")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	respond := func(context.Context, FlowKey, []byte) error { return nil }
	for _, classified := range []*ClassifiedFlow{route, route, preferred} {
		if err := server.ForwardUDP(ctx, classified, []byte("payload"), respond); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		select {
		case <-observed:
		case <-ctx.Done():
			t.Fatal("forwarded UDP packet did not reach the target")
		}
	}
	if requiredCalls.Load() != 1 || preferredCalls.Load() != 1 {
		t.Fatalf("required dials=%d preferred dials=%d; want one per association", requiredCalls.Load(), preferredCalls.Load())
	}
}

func TestUDPDatagramRequiredFailureNeverFallsBack(t *testing.T) {
	negotiationErr := protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "exit does not support native UDP datagrams")
	for _, supportsOptions := range []bool{false, true} {
		t.Run(fmt.Sprintf("options_supported=%t", supportsOptions), func(t *testing.T) {
			var legacyCalls, requiredCalls atomic.Int32
			legacy := &testDialer{udp: func(context.Context, string, string, uint16) (net.PacketConn, error) {
				legacyCalls.Add(1)
				return nil, errors.New("unexpected reliable stream fallback")
			}}
			var dialer Dialer = legacy
			if supportsOptions {
				dialer = &testUDPOptionsDialer{testDialer: legacy,
					udpWithOptions: func(_ context.Context, _, _ string, _ uint16, opts proxy.UDPDialOptions) (net.PacketConn, error) {
						requiredCalls.Add(1)
						if !opts.DatagramRequired {
							return nil, errors.New("required setting was not forwarded")
						}
						return nil, negotiationErr
					},
				}
			}
			server := newTestServer(t, Options{Dialer: dialer, Config: Config{Rules: []Rule{{
				Enabled: true, Process: "browser.exe", Action: ActionProxy, DatagramRequired: true,
			}}}})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// Both ready and cancellation can be signaled before ForwardUDP wakes.
			// Every failed association must retain the original rejection reason.
			const flows = 16
			for i := range flows {
				flow := testFlow(ProtoUDP, nil)
				flow.SourcePort += uint16(i)
				route, err := server.ClassifyFlow(flow)
				if err != nil {
					t.Fatal(err)
				}
				err = server.ForwardUDP(ctx, route, []byte("request"), func(context.Context, FlowKey, []byte) error { return nil })
				var relayErr *protocol.RelayError
				if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeDatagramRequired {
					t.Fatalf("required datagram rejection=%v", err)
				}
				if supportsOptions && !errors.Is(err, negotiationErr) {
					t.Fatalf("negotiation error was replaced: %v", err)
				}
			}
			if legacyCalls.Load() != 0 {
				t.Fatalf("required UDP silently used legacy dialing %d times", legacyCalls.Load())
			}
			if supportsOptions && requiredCalls.Load() != flows {
				t.Fatalf("required dials=%d; want %d", requiredCalls.Load(), flows)
			}
		})
	}
}
