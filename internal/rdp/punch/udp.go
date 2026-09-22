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

	"relayproxy/internal/protocol"
	"relayproxy/internal/rdp/secure"
)

const dataWireOverhead = 24 + 16
const punchKeepInterval = 10 * time.Second

type UDPResult struct {
	Conn       *net.UDPConn
	RemoteAddr *net.UDPAddr
	SessionID  uint64
	Key        []byte
	Domain     secure.Domain
}

// Punch races all validated UDP candidates on one socket and returns the
// authenticated peer address. The caller owns conn after a successful return.
func Punch(ctx context.Context, conn *net.UDPConn, candidates []protocol.RDPCandidate, sessionID uint64, key []byte, timeout time.Duration) (*UDPResult, error) {
	return PunchWithDomain(ctx, conn, candidates, sessionID, key, timeout, secure.DomainRDP)
}

func PunchWithDomain(
	ctx context.Context,
	conn *net.UDPConn,
	candidates []protocol.RDPCandidate,
	sessionID uint64,
	key []byte,
	timeout time.Duration,
	domain secure.Domain,
) (*UDPResult, error) {
	if conn == nil || sessionID == 0 || len(key) < 16 {
		return nil, errors.New("invalid RDP UDP punch session")
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
	seen := make(map[netip.AddrPort]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Protocol != "udp" {
			continue
		}
		address, err := netip.ParseAddrPort(candidate.Address)
		if err != nil {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return nil, errors.New("no usable RDP UDP candidates")
	}
	var nonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
		return nil, err
	}
	request, err := (secure.PunchPacket{Type: secure.PunchRequest, SessionID: sessionID, Nonce: nonce}).EncodeWithDomain(key, domain)
	if err != nil {
		return nil, err
	}
	nextSend := time.Time{}
	buffer := make([]byte, 1500)
	for {
		now := time.Now()
		if nextSend.IsZero() || !now.Before(nextSend) {
			if err := sendAll(conn, request, addresses); err != nil {
				return nil, err
			}
			nextSend = now.Add(80 * time.Millisecond)
		}
		readDeadline := nextSend
		if deadline.Before(readDeadline) {
			readDeadline = deadline
		}
		_ = conn.SetReadDeadline(readDeadline)
		n, remote, err := conn.ReadFromUDPAddrPort(buffer)
		if err == nil {
			remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
			packet, decodeErr := secure.DecodePunchPacketWithDomain(buffer[:n], key, domain)
			if decodeErr == nil && packet.Type == secure.PunchAck && packet.SessionID == sessionID && packet.Nonce == nonce {
				_ = conn.SetReadDeadline(time.Time{})
				return &UDPResult{
					Conn: conn, RemoteAddr: net.UDPAddrFromAddrPort(remote), SessionID: sessionID,
					Key: append([]byte(nil), key...), Domain: domain,
				}, nil
			}
		} else {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if time.Now().Before(deadline) {
					continue
				}
				return nil, fmt.Errorf("RDP UDP punch timeout: %w", err)
			}
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
}

func sendAll(conn *net.UDPConn, packet []byte, addresses []netip.AddrPort) error {
	for _, address := range addresses {
		if _, err := conn.WriteToUDPAddrPort(packet, address); err != nil {
			return err
		}
	}
	return nil
}

// DecodePunch extracts and authenticates a punch packet received on a shared
// target UDP endpoint. The session ID is public routing metadata; the token
// is still required before the packet is accepted.
func DecodePunch(raw []byte, key []byte) (secure.PunchPacket, error) {
	return secure.DecodePunchPacket(raw, key)
}

func DecodePunchWithDomain(raw []byte, key []byte, domain secure.Domain) (secure.PunchPacket, error) {
	return secure.DecodePunchPacketWithDomain(raw, key, domain)
}

func WritePunchAck(conn *net.UDPConn, addr *net.UDPAddr, request secure.PunchPacket, key []byte) error {
	return WritePunchAckWithDomain(conn, addr, request, key, secure.DomainRDP)
}

func WritePunchAckWithDomain(
	conn *net.UDPConn,
	addr *net.UDPAddr,
	request secure.PunchPacket,
	key []byte,
	domain secure.Domain,
) error {
	if request.Type != secure.PunchRequest && request.Type != secure.PunchKeep {
		return errors.New("not a punch request or keepalive")
	}
	packet, err := (secure.PunchPacket{
		Type: secure.PunchAck, SessionID: request.SessionID, Nonce: request.Nonce,
	}).EncodeWithDomain(key, domain)
	if err != nil {
		return err
	}
	_, err = conn.WriteToUDP(packet, addr)
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
	domain     secure.Domain
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
		return nil, errors.New("invalid RDP UDP result")
	}
	domain := result.Domain
	if domain == "" {
		domain = secure.DomainRDP
	}
	encode, err := secure.NewDataCodecWithDomain(result.SessionID, result.Key, domain)
	if err != nil {
		return nil, err
	}
	decode, err := secure.NewDataCodecWithDomain(result.SessionID, result.Key, domain)
	if err != nil {
		return nil, err
	}
	remotePort := result.RemoteAddr.AddrPort()
	if !remotePort.IsValid() {
		return nil, errors.New("invalid RDP UDP remote address")
	}
	remotePort = netip.AddrPortFrom(remotePort.Addr().Unmap(), remotePort.Port())
	c := &PacketConn{
		conn:       result.Conn,
		remote:     result.RemoteAddr,
		remotePort: remotePort,
		sessionID:  result.SessionID,
		key:        append([]byte(nil), result.Key...),
		domain:     domain,
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
			packet, err := (secure.PunchPacket{
				Type: secure.PunchKeep, SessionID: c.sessionID, Nonce: nonce,
			}).EncodeWithDomain(c.key, c.domain)
			if err != nil {
				return
			}
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

func (ioErrShortBuffer) Error() string { return "RDP UDP packet is larger than the receive buffer" }
