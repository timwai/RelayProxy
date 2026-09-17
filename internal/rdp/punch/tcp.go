package punch

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/rdp/secure"
)

const tcpPacketSize = 40

type tcpDialResult struct {
	conn net.Conn
	err  error
}

// LookupKey resolves a server-side in-memory RDP session token.
type LookupKey func(sessionID uint64) ([]byte, bool)

func Dial(ctx context.Context, candidates []protocol.RDPCandidate, sessionID uint64, key []byte, timeout time.Duration) (net.Conn, error) {
	if sessionID == 0 || len(key) < 16 {
		return nil, errors.New("invalid RDP TCP punch session")
	}
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	if candidateDeadline, ok := ctx.Deadline(); ok && candidateDeadline.Before(deadline) {
		deadline = candidateDeadline
	}
	addresses := make([]netip.AddrPort, 0, len(candidates))
	seen := make(map[netip.AddrPort]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Protocol != "tcp" {
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
		return nil, errors.New("no usable RDP TCP candidates")
	}
	attemptCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	results := make(chan tcpDialResult, len(addresses))
	for _, address := range addresses {
		address := address
		go func() {
			results <- dialTCPCandidate(attemptCtx, address, sessionID, key, deadline)
		}()
	}
	remaining := len(addresses)
	var lastErr error
wait:
	for remaining > 0 {
		select {
		case item := <-results:
			remaining--
			if item.err == nil && item.conn != nil {
				winner := item.conn
				cancel()
				go func(left int, winner net.Conn) {
					for i := 0; i < left; i++ {
						item := <-results
						if item.conn != nil && item.conn != winner {
							_ = item.conn.Close()
						}
					}
				}(remaining, winner)
				_ = winner.SetDeadline(time.Time{})
				return winner, nil
			}
			if item.err != nil {
				lastErr = item.err
			}
		case <-attemptCtx.Done():
			cancel()
			go func(left int) {
				for i := 0; i < left; i++ {
					item := <-results
					if item.conn != nil {
						_ = item.conn.Close()
					}
				}
			}(remaining)
			if lastErr == nil {
				lastErr = attemptCtx.Err()
			}
			break wait
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no usable RDP TCP candidates")
	}
	return nil, fmt.Errorf("RDP TCP punch failed: %w", lastErr)
}

func dialTCPCandidate(ctx context.Context, address netip.AddrPort, sessionID uint64, key []byte, deadline time.Time) tcpDialResult {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address.String())
	if err != nil {
		return tcpDialResult{err: err}
	}
	_ = conn.SetDeadline(deadline)
	var nonce uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &nonce); err != nil {
		_ = conn.Close()
		return tcpDialResult{err: err}
	}
	request := secure.PunchPacket{Type: secure.PunchRequest, SessionID: sessionID, Nonce: nonce}.Encode(key)
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return tcpDialResult{err: err}
	}
	var response [tcpPacketSize]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		_ = conn.Close()
		return tcpDialResult{err: err}
	}
	packet, err := secure.DecodePunchPacket(response[:], key)
	if err != nil || packet.Type != secure.PunchAck || packet.SessionID != sessionID || packet.Nonce != nonce {
		_ = conn.Close()
		if err == nil {
			err = errors.New("RDP TCP punch acknowledgement mismatch")
		}
		return tcpDialResult{err: err}
	}
	return tcpDialResult{conn: conn}
}

// Accept authenticates one accepted TCP connection.  The listener itself is
// bound by the agent and never accepts a caller-supplied host or port.
func Accept(ctx context.Context, conn net.Conn, lookup LookupKey, timeout time.Duration) (net.Conn, uint64, error) {
	if conn == nil || lookup == nil {
		return nil, 0, errors.New("invalid RDP TCP punch accept")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetDeadline(deadline)
	packet := make([]byte, tcpPacketSize)
	if _, err := io.ReadFull(conn, packet); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	if binary.BigEndian.Uint32(packet[:4]) != secure.PunchMagic {
		_ = conn.Close()
		return nil, 0, errors.New("invalid RDP TCP punch magic")
	}
	sessionID := binary.BigEndian.Uint64(packet[6:14])
	key, ok := lookup(sessionID)
	if !ok {
		_ = conn.Close()
		return nil, 0, errors.New("unknown RDP TCP punch session")
	}
	punch, err := secure.DecodePunchPacket(packet, key)
	if err != nil || punch.Type != secure.PunchRequest {
		_ = conn.Close()
		if err == nil {
			err = errors.New("invalid RDP TCP punch request")
		}
		return nil, 0, err
	}
	if _, err := conn.Write(secure.PunchPacket{Type: secure.PunchAck, SessionID: sessionID, Nonce: punch.Nonce}.Encode(key)); err != nil {
		_ = conn.Close()
		return nil, 0, err
	}
	_ = conn.SetDeadline(time.Time{})
	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil, 0, ctx.Err()
	default:
		return conn, sessionID, nil
	}
}
