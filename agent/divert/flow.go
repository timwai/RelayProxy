package divert

import (
	"errors"
	"fmt"
	"net/netip"
	"relayproxy/internal/traffic"
	"strings"
	"sync/atomic"
)

var (
	ErrClosed            = errors.New("divert server is closed")
	ErrFlowCapacity      = errors.New("divert flow capacity reached")
	ErrAssociationClosed = errors.New("divert UDP association is closed")
	ErrNotProxyFlow      = errors.New("divert: the platform must release DIRECT flows and drop REJECT flows")
)

// FlowKey includes the complete original five-tuple, never the redirected
// listener address. One UDP socket may communicate with several destinations.
type FlowKey struct {
	Protocol    Protocol
	Source      netip.AddrPort
	Destination netip.AddrPort
}

// ClassifiedFlow is an immutable policy result belonging to one Server.
// Platform adapters classify before interception, release DIRECT unchanged,
// drop REJECT, and pass only PROXY flows to ForwardTCP/ForwardUDP. A TCP token
// is consumed once. A UDP token is reused until its association expires.
type ClassifiedFlow struct {
	owner    *Server
	key      FlowKey
	flow     Flow
	decision Decision
	udp      *udpAssociation
	used     atomic.Bool
	traffic  *traffic.Record
}

func (f *ClassifiedFlow) Key() FlowKey       { return f.key }
func (f *ClassifiedFlow) Metadata() Flow     { return f.flow }
func (f *ClassifiedFlow) Decision() Decision { return f.decision }

func validateFlow(flow Flow) (Flow, FlowKey, error) {
	if flow.Protocol != ProtoTCP && flow.Protocol != ProtoUDP {
		return Flow{}, FlowKey{}, fmt.Errorf("divert: unsupported flow protocol %q", flow.Protocol)
	}
	if strings.TrimSpace(flow.Process) == "" {
		return Flow{}, FlowKey{}, errors.New("divert: original process identity is required")
	}
	source, err := netip.ParseAddr(flow.SourceIP)
	if err != nil || source.IsUnspecified() || flow.SourcePort == 0 {
		return Flow{}, FlowKey{}, errors.New("divert: valid original source IP and port are required")
	}
	destination, err := netip.ParseAddr(flow.IP)
	if err != nil || destination.IsUnspecified() || flow.Port == 0 {
		return Flow{}, FlowKey{}, errors.New("divert: valid original destination IP and port are required")
	}
	source, destination = source.Unmap(), destination.Unmap()
	flow.SourceIP, flow.IP = source.String(), destination.String()
	flow.Process = strings.TrimSpace(flow.Process)
	flow.ProcessAliases = cleanIdentityList(flow.ProcessAliases)
	flow.Services = cleanIdentityList(flow.Services)
	flow.Host = strings.TrimSpace(flow.Host)
	return flow, FlowKey{
		Protocol:    flow.Protocol,
		Source:      netip.AddrPortFrom(source, flow.SourcePort),
		Destination: netip.AddrPortFrom(destination, flow.Port),
	}, nil
}


func cleanIdentityList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}
