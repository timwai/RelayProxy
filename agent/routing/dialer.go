package routing

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"relayproxy/agent/client"
	"relayproxy/internal/protocol"
	"relayproxy/internal/proxy"
	"relayproxy/internal/traffic"
)

// RoutingDialer routes connections based on the rule engine.
// It implements proxy.TunnelDialer.
type RoutingDialer struct {
	engine       *Engine
	tunnel       proxy.TunnelDialer // Underlying tunnel dialer for PROXY action
	directDialer net.Dialer         // Direct dialer for DIRECT action
	policyMu     *sync.RWMutex
	// Set before accepting connections.
	Traffic       *traffic.Registry
	LookupProcess func(string, netip.AddrPort, netip.AddrPort) (uint32, string, error)
}

// NewRoutingDialer creates a RoutingDialer that wraps the given tunnel dialer.
func NewRoutingDialer(engine *Engine, tunnel proxy.TunnelDialer, policyMu ...*sync.RWMutex) *RoutingDialer {
	d := &RoutingDialer{
		engine: engine,
		tunnel: tunnel,
	}
	if len(policyMu) > 0 {
		d.policyMu = policyMu[0]
	}
	return d
}

func (d *RoutingDialer) decide(host string, port uint16) Decision {
	return d.decideFlow(Flow{Host: host, Port: port, Protocol: "tcp"})
}

func (d *RoutingDialer) decideFlow(flow Flow) Decision {
	if d.policyMu != nil {
		d.policyMu.RLock()
		defer d.policyMu.RUnlock()
	}
	return d.engine.DecideFlow(flow)
}

func (d *RoutingDialer) begin(ctx context.Context, exitID, host string, port uint16, protocol string) (Decision, *traffic.Record) {
	info := proxy.ClientFromContext(ctx)
	flow := Flow{Host: host, Port: port, Protocol: protocol}
	var pid uint32
	if d.LookupProcess != nil && info.Source.IsValid() && info.Local.IsValid() {
		if id, process, err := d.LookupProcess(protocol, info.Source, info.Local); err == nil {
			pid, flow.Process = id, process
		}
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		flow.IP = ip.Unmap().String()
	}
	decision := d.decideFlow(flow)
	if decision.Action == ActionProxy {
		if decision.ExitID != "" {
			exitID = decision.ExitID
		}
		if exitID == "" {
			if td, ok := d.tunnel.(*client.TunnelDialer); ok {
				exitID = td.GetDefaultExitID()
			}
		}
		decision.ExitID = exitID
	} else {
		exitID = ""
	}
	meta := traffic.Metadata{ProcessID: pid, Process: flow.Process, Host: host, IP: flow.IP, Port: port,
		DomainSource: "requested", Protocol: protocol, Entry: info.Entry, Action: string(decision.Action), Rule: decision.Rule, ExitID: exitID}
	if info.Source.IsValid() {
		meta.Source = info.Source.String()
	}
	return decision, d.Traffic.Start(meta)
}

func finishDial(record *traffic.Record, decision Decision, err error) {
	state := "failed"
	if decision.Action == ActionReject {
		state = "rejected"
	}
	record.Finish(state, err)
}

// Engine returns the underlying routing engine for hot-reload.
func (d *RoutingDialer) Engine() *Engine {
	return d.engine
}

func (d *RoutingDialer) SetDefaultExitID(exitID string) {
	if td, ok := d.tunnel.(*client.TunnelDialer); ok {
		td.SetDefaultExitID(exitID)
	}
}

// DialTCP implements proxy.TunnelDialer.
// It evaluates routing rules before deciding how to connect.
func (d *RoutingDialer) DialTCP(ctx context.Context, exitNodeID string, host string, port uint16) (net.Conn, error) {
	decision, record := d.begin(ctx, exitNodeID, host, port, "tcp")
	conn, err := d.dialTCP(ctx, exitNodeID, host, port, decision)
	if err != nil {
		finishDial(record, decision, err)
		return nil, err
	}
	if conn == nil {
		err = fmt.Errorf("routing: TCP dialer returned a nil connection")
		finishDial(record, decision, err)
		return nil, err
	}
	if decision.Action == ActionDirect {
		if addr := conn.RemoteAddr(); addr != nil {
			if ip, _, err := net.SplitHostPort(addr.String()); err == nil {
				record.SetIP(ip)
			}
		}
	}
	return traffic.WrapConn(conn, record), nil
}

func (d *RoutingDialer) dialTCP(ctx context.Context, exitNodeID, host string, port uint16, decision Decision) (net.Conn, error) {
	action, ruleExitID := decision.Action, decision.ExitID

	switch action {
	case ActionDirect:
		addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
		log.Printf("[Routing] DIRECT %s", addr)
		return d.directDialer.DialContext(ctx, "tcp", addr)

	case ActionReject:
		log.Printf("[Routing] REJECT %s:%d", host, port)
		return nil, fmt.Errorf("connection to %s:%d blocked by routing rule", host, port)

	case ActionProxy:
		eid := exitNodeID
		if ruleExitID != "" {
			eid = ruleExitID
		}
		log.Printf("[Routing] PROXY %s:%d (exit=%s)", host, port, eid)
		return d.tunnel.DialTCP(ctx, eid, host, port)

	default:
		return nil, fmt.Errorf("unsupported routing action %q", action)
	}
}

// DialUDP implements proxy.TunnelDialer.
func (d *RoutingDialer) DialUDP(ctx context.Context, exitNodeID string, host string, port uint16) (net.PacketConn, error) {
	decision, record := d.begin(ctx, exitNodeID, host, port, "udp")
	pc, err := d.dialUDP(ctx, exitNodeID, host, port, decision)
	if err != nil {
		finishDial(record, decision, err)
		return nil, err
	}
	if pc == nil {
		err = fmt.Errorf("routing: UDP dialer returned a nil connection")
		finishDial(record, decision, err)
		return nil, err
	}
	return traffic.WrapPacketConn(pc, record), nil
}

func (d *RoutingDialer) dialUDP(ctx context.Context, exitNodeID, host string, port uint16, decision Decision) (net.PacketConn, error) {
	action, ruleExitID := decision.Action, decision.ExitID

	switch action {
	case ActionDirect:
		addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
		log.Printf("[Routing] DIRECT UDP %s", addr)
		c, err := d.directDialer.DialContext(ctx, "udp", addr)
		if err != nil {
			return nil, err
		}
		pc, ok := c.(*net.UDPConn)
		if !ok {
			_ = c.Close()
			return nil, fmt.Errorf("udp dial did not return UDPConn")
		}
		wrapped, err := proxy.WrapConnectedUDP(pc)
		if err != nil {
			_ = pc.Close()
			return nil, err
		}
		return wrapped, nil

	case ActionReject:
		log.Printf("[Routing] REJECT UDP %s:%d", host, port)
		return nil, fmt.Errorf("connection to %s:%d blocked by routing rule", host, port)

	case ActionProxy:
		eid := exitNodeID
		if ruleExitID != "" {
			eid = ruleExitID
		}
		log.Printf("[Routing] PROXY UDP %s:%d (exit=%s)", host, port, eid)
		if decision.DatagramRequired {
			optionsDialer, ok := d.tunnel.(proxy.UDPOptionsDialer)
			if !ok {
				return nil, protocol.NewRelayError(protocol.ErrCodeDatagramRequired, "native UDP datagrams are required by routing policy")
			}
			return optionsDialer.DialUDPWithOptions(ctx, eid, host, port, proxy.UDPDialOptions{DatagramRequired: true})
		}
		return d.tunnel.DialUDP(ctx, eid, host, port)

	default:
		return nil, fmt.Errorf("unsupported routing action %q", action)
	}
}
