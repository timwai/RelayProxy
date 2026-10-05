package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/p2p/secure"
	p2pupnp "relayproxy/internal/p2p/upnp"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// Endpoint owns the UDP socket that must be reused across reflexive discovery,
// hole punching and the future QUIC transport. Replacing this socket changes
// the NAT mapping and would invalidate the candidates sent through the server.
type Endpoint struct {
	mu          sync.RWMutex
	rendezvous  string
	portStart   int
	portEnd     int
	upnpEnabled bool
	conn        *net.UDPConn
	identity    *secure.TLSIdentity
	candidates  []protocol.P2PCandidate
	upnpMapping *p2pupnp.Mapping
	closed      bool
}

var (
	endpointPortCursor atomic.Uint32
	mapUPnPUDP         = p2pupnp.MapUDP
)

func NewEndpoint(rendezvous string) *Endpoint {
	return NewEndpointWithPortRange(rendezvous, 0, 0)
}

func NewEndpointWithPortRange(rendezvous string, portStart, portEnd int) *Endpoint {
	return NewEndpointWithPortRangeAndUPnP(rendezvous, portStart, portEnd, false)
}

func NewEndpointWithPortRangeAndUPnP(rendezvous string, portStart, portEnd int, upnpEnabled bool) *Endpoint {
	return &Endpoint{rendezvous: rendezvous, portStart: portStart, portEnd: portEnd, upnpEnabled: upnpEnabled}
}

func listenP2PUDP(portStart, portEnd int) (*net.UDPConn, error) {
	if portStart == 0 && portEnd == 0 {
		return net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	}
	if portStart < 1 || portStart > 65535 || portEnd < portStart || portEnd > 65535 {
		return nil, fmt.Errorf("invalid P2P UDP port range %d-%d", portStart, portEnd)
	}

	count := portEnd - portStart + 1
	offset := int(endpointPortCursor.Add(1)-1) % count
	var lastErr error
	for i := 0; i < count; i++ {
		port := portStart + (offset+i)%count
		conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no available UDP port in P2P range %d-%d: %w", portStart, portEnd, lastErr)
}

func (e *Endpoint) Start(ctx context.Context) error {
	if e == nil {
		return errors.New("nil P2P endpoint")
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return net.ErrClosed
	}
	if e.conn != nil {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		return err
	}
	// Use Go's wildcard "udp" listener so supported platforms get one dual-stack
	// socket. Candidate discovery, punching and QUIC must all keep this exact
	// socket to preserve the NAT mapping.
	conn, err := listenP2PUDP(e.portStart, e.portEnd)
	if err != nil {
		return err
	}
	tunnel.TuneUDPConn(conn)
	port := conn.LocalAddr().(*net.UDPAddr).Port
	discovered := candidate.Discover(port, 0)

	type upnpResult struct {
		mapping *p2pupnp.Mapping
		address netip.AddrPort
		err     error
	}
	var upnpResultCh chan upnpResult
	if e.upnpEnabled {
		upnpResultCh = make(chan upnpResult, 1)
		go func() {
			mapCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			mapping, address, mapErr := mapUPnPUDP(mapCtx, port)
			upnpResultCh <- upnpResult{mapping: mapping, address: address, err: mapErr}
		}()
	}

	if e.rendezvous != "" {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		reflexive, probeErr := candidate.ProbeReflexive(probeCtx, e.rendezvous, conn, "udp")
		cancel()
		if probeErr == nil {
			discovered = appendEndpointCandidate(discovered, reflexive)
		}
	}

	var upnpMapping *p2pupnp.Mapping
	if upnpResultCh != nil {
		result := <-upnpResultCh
		if result.err == nil && result.address.IsValid() {
			upnpMapping = result.mapping
			discovered = appendEndpointCandidate(discovered, protocol.P2PCandidate{
				Protocol: "udp", Type: "reflexive", Address: result.address.String(), Priority: 900,
			})
		}
	}
	if validated, validateErr := candidate.Validate(discovered); validateErr == nil {
		discovered = validated
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		if upnpMapping != nil {
			_ = upnpMapping.Close()
		}
		_ = conn.Close()
		return net.ErrClosed
	}
	if e.conn != nil {
		e.mu.Unlock()
		if upnpMapping != nil {
			_ = upnpMapping.Close()
		}
		_ = conn.Close()
		return nil
	}
	e.conn = conn
	e.identity = identity
	e.candidates = append([]protocol.P2PCandidate(nil), discovered...)
	e.upnpMapping = upnpMapping
	e.mu.Unlock()
	return nil
}

func appendEndpointCandidate(items []protocol.P2PCandidate, extra protocol.P2PCandidate) []protocol.P2PCandidate {
	validated, err := candidate.Validate([]protocol.P2PCandidate{extra})
	if err != nil || len(validated) != 1 {
		return items
	}
	extra = validated[0]
	for _, item := range items {
		if item.Protocol == extra.Protocol && item.Address == extra.Address {
			return items
		}
	}
	items = append(items, extra)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].Address < items[j].Address
	})
	if len(items) > candidate.MaxCandidates {
		items = items[:candidate.MaxCandidates]
	}
	return items
}

func (e *Endpoint) Description() ([]protocol.P2PCandidate, string, error) {
	if e == nil {
		return nil, "", errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, "", net.ErrClosed
	}
	if e.conn == nil || e.identity == nil {
		return nil, "", errors.New("P2P endpoint is not started")
	}
	if len(e.candidates) == 0 {
		return nil, "", errors.New("P2P endpoint has no usable UDP candidate")
	}
	return append([]protocol.P2PCandidate(nil), e.candidates...), e.identity.Fingerprint, nil
}

func (e *Endpoint) UDPConn() (*net.UDPConn, error) {
	if e == nil {
		return nil, errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.conn == nil {
		return nil, net.ErrClosed
	}
	return e.conn, nil
}

func (e *Endpoint) Identity() (*secure.TLSIdentity, error) {
	if e == nil {
		return nil, errors.New("nil P2P endpoint")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.identity == nil {
		return nil, net.ErrClosed
	}
	return e.identity, nil
}

func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	conn := e.conn
	mapping := e.upnpMapping
	e.conn = nil
	e.identity = nil
	e.candidates = nil
	e.upnpMapping = nil
	e.mu.Unlock()
	if mapping != nil {
		_ = mapping.Close()
	}
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// CurrentNetworkSignature returns a stable, non-reversible signature of the
// currently usable non-loopback interface addresses. It is used only to detect
// local network changes; raw addresses are never reported through telemetry.
func CurrentNetworkSignature() string {
	interfaces, _ := net.Interfaces()
	values := make([]string, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, raw := range addrs {
			var ip netip.Addr
			switch value := raw.(type) {
			case *net.IPNet:
				if parsed, ok := netip.AddrFromSlice(value.IP); ok {
					ip = parsed.Unmap()
				}
			case *net.IPAddr:
				if parsed, ok := netip.AddrFromSlice(value.IP); ok {
					ip = parsed.Unmap()
				}
			}
			if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
				continue
			}
			values = append(values, iface.Name+"="+ip.String())
		}
	}
	sort.Strings(values)
	sum := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(sum[:16])
}
