package punch

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
)

const dataWireOverhead = 24 + 16
const punchKeepInterval = 10 * time.Second
const punchSelectionWindow = 120 * time.Millisecond
const candidatePriorityRTTWeight = int64(2)

type UDPResult struct {
	Conn              *net.UDPConn
	RemoteAddr        *net.UDPAddr
	SessionID         uint64
	Key               []byte
	CandidatePriority uint32
	PunchRTT          time.Duration
}

type punchObservation struct {
	priority       uint32
	gotAck         bool
	sawPeerRequest bool
	ready          bool
	rtt            time.Duration
}

// Punch races all validated UDP candidates on one socket and returns the
// authenticated peer address. The caller owns conn after a successful return.
func Punch(ctx context.Context, conn *net.UDPConn, candidates []protocol.P2PCandidate, sessionID uint64, key []byte, timeout time.Duration) (*UDPResult, error) {
	return punch(ctx, conn, candidates, sessionID, key, timeout, true)
}

// PunchResponder dials a passive authenticated responder, such as an RDP
// target. Its ACK of our nonce proves round-trip reachability; it need not send
// a request of its own. Symmetric QUIC peers must continue to use Punch so both
// sides finish their handshake before either starts reading QUIC packets.
func PunchResponder(ctx context.Context, conn *net.UDPConn, candidates []protocol.P2PCandidate, sessionID uint64, key []byte, timeout time.Duration) (*UDPResult, error) {
	return punch(ctx, conn, candidates, sessionID, key, timeout, false)
}

func punch(ctx context.Context, conn *net.UDPConn, candidates []protocol.P2PCandidate, sessionID uint64, key []byte, timeout time.Duration, symmetric bool) (*UDPResult, error) {
	if conn == nil || sessionID == 0 || len(key) < 16 {
		return nil, errors.New("invalid P2P UDP punch session")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	defer conn.SetReadDeadline(time.Time{})
	addresses := make([]netip.AddrPort, 0, len(candidates))
	observations := make(map[netip.AddrPort]*punchObservation, len(candidates))
	for _, candidate := range candidates {
		if candidate.Protocol != "udp" {
			continue
		}
		address, err := netip.ParseAddrPort(candidate.Address)
		if err != nil {
			continue
		}
		address = netip.AddrPortFrom(address.Addr().Unmap(), address.Port())
		observation, exists := observations[address]
		if !exists {
			observations[address] = &punchObservation{priority: candidate.Priority}
			addresses = append(addresses, address)
			continue
		}
		if candidate.Priority > observation.priority {
			observation.priority = candidate.Priority
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("no usable P2P UDP candidates")
	}
	var nextNonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nextNonce); err != nil {
		return nil, err
	}
	pending := make(map[uint64]time.Time, len(addresses)*2)
	nextSend := time.Time{}
	buffer := make([]byte, 1500)
	var settleUntil time.Time
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		if (!settleUntil.IsZero() && !now.Before(settleUntil)) || !now.Before(deadline) {
			if remote, observation, ok := bestPunchObservation(observations); ok {
				_ = conn.SetReadDeadline(time.Time{})
				return &UDPResult{
					Conn: conn, RemoteAddr: net.UDPAddrFromAddrPort(remote),
					SessionID: sessionID, Key: append([]byte(nil), key...),
					CandidatePriority: observation.priority, PunchRTT: observation.rtt,
				}, nil
			}
			return nil, fmt.Errorf("P2P UDP punch timeout after %s", timeout)
		}
		if nextSend.IsZero() || !now.Before(nextSend) {
			if err := sendPunchRound(conn, sessionID, key, addresses, &nextNonce, pending); err != nil {
				return nil, err
			}
			nextSend = time.Now().Add(80 * time.Millisecond)
		}
		readDeadline := nextSend
		if !settleUntil.IsZero() && settleUntil.Before(readDeadline) {
			readDeadline = settleUntil
		}
		if deadline.Before(readDeadline) {
			readDeadline = deadline
		}
		_ = conn.SetReadDeadline(readDeadline)
		n, remote, err := conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return nil, err
		}
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		packet, decodeErr := secure.DecodePunchPacket(buffer[:n], key)
		if decodeErr != nil || packet.SessionID != sessionID {
			continue
		}
		observation := observations[remote]
		if observation == nil {
			// A valid authenticated peer can appear from a mapping that differs
			// from rendezvous. Keep it usable, but below advertised candidates.
			observation = &punchObservation{}
			observations[remote] = observation
		}
		switch packet.Type {
		case secure.PunchRequest, secure.PunchKeep:
			observation.sawPeerRequest = true
			ack := secure.PunchPacket{Type: secure.PunchAck, SessionID: sessionID, Nonce: packet.Nonce}.Encode(key)
			_, _ = conn.WriteToUDPAddrPort(ack, remote)
		case secure.PunchAck:
			if sentAt, ok := pending[packet.Nonce]; ok {
				delete(pending, packet.Nonce)
				observation.gotAck = true
				rtt := time.Since(sentAt)
				if observation.rtt <= 0 || rtt < observation.rtt {
					observation.rtt = rtt
				}
			}
		}
		if observation.gotAck && (!symmetric || observation.sawPeerRequest) && !observation.ready {
			observation.ready = true
			if settleUntil.IsZero() {
				settleUntil = time.Now().Add(punchSelectionWindow)
				if deadline.Before(settleUntil) {
					settleUntil = deadline
				}
			}
			// A sole advertised responder leaves no alternative to compare.
			// Avoid the selection delay for this common LAN RDP case, but keep
			// the window for symmetric peers and multiple candidate addresses.
			if !symmetric && len(addresses) == 1 && remote == addresses[0] {
				settleUntil = time.Now()
			}
		}
	}
}

func candidateScore(priority uint32, rtt time.Duration) int64 {
	latencyMs := rtt.Milliseconds()
	if latencyMs < 0 {
		latencyMs = 0
	}
	if latencyMs > 1000 {
		latencyMs = 1000
	}
	return int64(priority)*candidatePriorityRTTWeight - latencyMs
}

func bestPunchObservation(observations map[netip.AddrPort]*punchObservation) (netip.AddrPort, *punchObservation, bool) {
	var bestAddr netip.AddrPort
	var best *punchObservation
	var bestScore int64
	for address, observation := range observations {
		if observation == nil || !observation.ready {
			continue
		}
		score := candidateScore(observation.priority, observation.rtt)
		if best == nil || score > bestScore ||
			(score == bestScore && (observation.rtt < best.rtt ||
				(observation.rtt == best.rtt && address.String() < bestAddr.String()))) {
			bestAddr, best, bestScore = address, observation, score
		}
	}
	return bestAddr, best, best != nil
}

func sendPunchRound(conn *net.UDPConn, sessionID uint64, key []byte, addresses []netip.AddrPort, nextNonce *uint64, pending map[uint64]time.Time) error {
	var lastErr error
	sent := 0
	for _, address := range addresses {
		*nextNonce = *nextNonce + 1
		nonce := *nextNonce
		packet := secure.PunchPacket{Type: secure.PunchRequest, SessionID: sessionID, Nonce: nonce}.Encode(key)
		if _, err := conn.WriteToUDPAddrPort(packet, address); err != nil {
			lastErr = err
			continue
		}
		pending[nonce] = time.Now()
		sent++
	}
	if sent > 0 {
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("no usable P2P UDP candidates")
}

func sendAll(conn *net.UDPConn, packet []byte, addresses []netip.AddrPort) error {
	var lastErr error
	sent := 0
	for _, address := range addresses {
		if _, err := conn.WriteToUDPAddrPort(packet, address); err != nil {
			// Mixed IPv4/IPv6 candidate sets are expected. A socket may reject
			// one family on a platform without dual-stack support, but another
			// candidate can still succeed and must be allowed to race.
			lastErr = err
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("no usable P2P UDP candidates")
}

// DecodePunch extracts and authenticates a punch packet received on a shared
// target UDP endpoint. The session ID is public routing metadata; the token
// is still required before the packet is accepted.
func DecodePunch(raw []byte, key []byte) (secure.PunchPacket, error) {
	return secure.DecodePunchPacket(raw, key)
}

func WritePunchAck(conn *net.UDPConn, addr *net.UDPAddr, request secure.PunchPacket, key []byte) error {
	if request.Type != secure.PunchRequest && request.Type != secure.PunchKeep {
		return errors.New("not a punch request or keepalive")
	}
	_, err := conn.WriteToUDP(secure.PunchPacket{Type: secure.PunchAck, SessionID: request.SessionID, Nonce: request.Nonce}.Encode(key), addr)
	return err
}

// PacketConn wraps an authenticated UDP association as a net.PacketConn. It
// deliberately ignores caller-provided destinations: the coordinator's
// candidate exchange fixed the peer address for this session.
type PacketConn struct {
	conn       *net.UDPConn
	remote     *net.UDPAddr
	remotePort netip.AddrPort
	sessionID  uint64
	key        []byte
	encode     *secure.DataCodec
	decode     *secure.DataCodec
	readMu     sync.Mutex
	writeMu    sync.Mutex
	closeOnce  sync.Once
	done       chan struct{}
	packetID   atomic.Uint32
	reassembly *Reassembler
	readFrame  []byte
	readWire   []byte
	writeWire  []byte
}

func NewPacketConn(result *UDPResult) (*PacketConn, error) {
	return newPacketConn(result, punchKeepInterval)
}

func newPacketConn(result *UDPResult, keepInterval time.Duration) (*PacketConn, error) {
	if result == nil || result.Conn == nil || result.RemoteAddr == nil {
		return nil, errors.New("invalid P2P UDP result")
	}
	encode, err := secure.NewDataCodec(result.SessionID, result.Key)
	if err != nil {
		return nil, err
	}
	decode, err := secure.NewDataCodec(result.SessionID, result.Key)
	if err != nil {
		return nil, err
	}
	remotePort := result.RemoteAddr.AddrPort()
	if !remotePort.IsValid() {
		return nil, errors.New("invalid P2P UDP remote address")
	}
	remotePort = netip.AddrPortFrom(remotePort.Addr().Unmap(), remotePort.Port())
	c := &PacketConn{
		conn:       result.Conn,
		remote:     result.RemoteAddr,
		remotePort: remotePort,
		sessionID:  result.SessionID,
		key:        append([]byte(nil), result.Key...),
		encode:     encode,
		decode:     decode,
		done:       make(chan struct{}),
		reassembly: NewReassembler(),
		readFrame:  make([]byte, protocol.UDPFragmentHeaderSize+protocol.UDPFragmentPayload),
		readWire:   make([]byte, secure.MaxDataPayload+dataWireOverhead),
		writeWire:  make([]byte, secure.MaxDataPayload+dataWireOverhead),
	}
	if keepInterval > 0 {
		go c.keepaliveLoop(keepInterval)
	}
	return c, nil
}

func (c *PacketConn) keepaliveLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var nonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
		nonce = uint64(time.Now().UnixNano())
	}
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			nonce++
			packet := secure.PunchPacket{Type: secure.PunchKeep, SessionID: c.sessionID, Nonce: nonce}.Encode(c.key)
			if _, err := c.conn.WriteToUDPAddrPort(packet, c.remotePort); err != nil {
				return
			}
		}
	}
}

func (c *PacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	wire := c.readWire
	for {
		n, remote, err := c.conn.ReadFromUDPAddrPort(wire)
		if err != nil {
			return 0, nil, err
		}
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if remote != c.remotePort {
			continue
		}
		decoded, err := c.decode.DecodeTo(wire[:n], c.readFrame)
		if err != nil {
			continue
		}
		assembled, complete, err := c.reassembly.Feed(c.readFrame[:decoded], buffer)
		if _, short := err.(ioErrShortBuffer); short {
			return 0, c.remote, err
		}
		if err != nil || !complete {
			continue
		}
		return assembled, c.remote, nil
	}
}

func (c *PacketConn) WriteTo(payload []byte, _ net.Addr) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	packetID := c.packetID.Add(1)
	if err := FragmentUDP(packetID, payload, func(frame []byte) error {
		wire, err := c.encode.EncodeTo(c.writeWire[:0], frame)
		if err != nil {
			return err
		}
		_, err = c.conn.WriteToUDPAddrPort(wire, c.remotePort)
		return err
	}); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (c *PacketConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		c.reassembly.Close()
		err = c.conn.Close()
	})
	return err
}
func (c *PacketConn) LocalAddr() net.Addr                { return c.conn.LocalAddr() }
func (c *PacketConn) SetDeadline(t time.Time) error      { return c.conn.SetDeadline(t) }
func (c *PacketConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *PacketConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

type ioErrShortBuffer struct{}

func (ioErrShortBuffer) Error() string { return "P2P UDP packet is larger than the receive buffer" }
