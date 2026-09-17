package rdp

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"relayproxy/internal/rdp/candidate"
	"relayproxy/internal/tunnel"
)

// Rendezvous is a tiny UDP observer used only to discover the source address
// of an agent's outbound UDP socket. It does not forward application data.
type Rendezvous struct {
	conn      *net.UDPConn
	maxPerMin int
	mu        sync.Mutex
	counts    map[netip.Addr]windowCounter
	cancel    context.CancelFunc
	closeOnce sync.Once
}

type windowCounter struct {
	started time.Time
	count   int
}

func StartRendezvous(ctx context.Context, address string, maxPerMinute int) (*Rendezvous, error) {
	if address == "" {
		return nil, nil
	}
	if maxPerMinute <= 0 {
		maxPerMinute = 120
	}
	udpAddr, err := net.ResolveUDPAddr("udp", address)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("RDP rendezvous listen: %w", err)
	}
	tunnel.TuneUDPConn(conn)
	workCtx, cancel := context.WithCancel(ctx)
	r := &Rendezvous{conn: conn, maxPerMin: maxPerMinute, counts: make(map[netip.Addr]windowCounter), cancel: cancel}
	go r.serve(workCtx)
	return r, nil
}

func (r *Rendezvous) Addr() net.Addr {
	if r == nil || r.conn == nil {
		return nil
	}
	return r.conn.LocalAddr()
}

func (r *Rendezvous) Close() error {
	if r == nil {
		return nil
	}
	var err error
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		err = r.conn.Close()
	})
	return err
}

func (r *Rendezvous) serve(ctx context.Context) {
	buffer := make([]byte, 64)
	for {
		_ = r.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, remote, err := r.conn.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("[RDP] rendezvous read failed: %v", err)
			return
		}
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if n != 16 || binary.BigEndian.Uint32(buffer[0:4]) != candidate.ProbeMagic || buffer[4] != candidate.ProbeVersion || !r.allowAddr(remote.Addr()) {
			continue
		}
		var response [32]byte
		binary.BigEndian.PutUint32(response[0:4], candidate.ProbeMagic)
		response[4] = candidate.ProbeVersion
		binary.BigEndian.PutUint16(response[6:8], remote.Port())
		copy(response[8:16], buffer[8:16])
		ip := remote.Addr().As16()
		copy(response[16:32], ip[:])
		_, _ = r.conn.WriteToUDPAddrPort(response[:], remote)
	}
}

func (r *Rendezvous) allow(ip string) bool {
	parsed, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return r.allowAddr(parsed)
}

func (r *Rendezvous) allowAddr(ip netip.Addr) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	item := r.counts[ip]
	if item.started.IsZero() || now.Sub(item.started) >= time.Minute {
		item = windowCounter{started: now}
	}
	if item.count >= r.maxPerMin {
		return false
	}
	item.count++
	r.counts[ip] = item
	if len(r.counts) > 8192 {
		for key, value := range r.counts {
			if now.Sub(value.started) > time.Minute {
				delete(r.counts, key)
			}
		}
	}
	return true
}
