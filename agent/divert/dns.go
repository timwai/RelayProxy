package divert

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const dnsCapacity = 4096

type dnsQueryKey struct {
	client, server netip.AddrPort
	id             uint16
	protocol       Protocol
}
type dnsQuestion struct {
	name    string
	kind    dnsmessage.Type
	expires time.Time
}
type dnsNames struct {
	names          map[string]time.Time
	ambiguousUntil time.Time
}

// dnsAssociations is deliberately conservative: only matched, observed DNS
// exchanges populate it. Shared IPs with several live names stay unidentified.
// This is an association, never proof that an application requested that name.
type dnsAssociations struct {
	mu        sync.Mutex
	now       func() time.Time
	pending   map[dnsQueryKey]dnsQuestion
	addresses map[netip.Addr]*dnsNames
}

func newDNSAssociations() *dnsAssociations {
	return &dnsAssociations{now: time.Now, pending: make(map[dnsQueryKey]dnsQuestion), addresses: make(map[netip.Addr]*dnsNames)}
}

func dnsName(name dnsmessage.Name) string {
	return strings.ToLower(strings.TrimSuffix(name.String(), "."))
}

func (d *dnsAssociations) query(client, server netip.AddrPort, payload []byte) {
	d.queryProtocol(ProtoUDP, client, server, payload)
}

func (d *dnsAssociations) queryProtocol(protocol Protocol, client, server netip.AddrPort, payload []byte) {
	if server.Port() != 53 {
		return
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(payload); err != nil || msg.Response || msg.OpCode != 0 || len(msg.Questions) != 1 {
		return
	}
	q := msg.Questions[0]
	if q.Class != dnsmessage.ClassINET || (q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.prune(now)
	key := dnsQueryKey{client: client, server: server, id: msg.ID, protocol: protocol}
	if len(d.pending) >= dnsCapacity {
		return
	}
	d.pending[key] = dnsQuestion{name: dnsName(q.Name), kind: q.Type, expires: now.Add(10 * time.Second)}
}

func (d *dnsAssociations) response(server, client netip.AddrPort, payload []byte) {
	d.responseProtocol(ProtoUDP, server, client, payload)
}

func (d *dnsAssociations) responseProtocol(protocol Protocol, server, client netip.AddrPort, payload []byte) {
	if server.Port() != 53 {
		return
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(payload); err != nil || !msg.Response || msg.OpCode != 0 || msg.Truncated || msg.RCode != dnsmessage.RCodeSuccess || len(msg.Questions) != 1 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	key := dnsQueryKey{client: client, server: server, id: msg.ID, protocol: protocol}
	pending, ok := d.pending[key]
	q := msg.Questions[0]
	if !ok || !pending.expires.After(now) || pending.name != dnsName(q.Name) || pending.kind != q.Type || q.Class != dnsmessage.ClassINET {
		return
	}
	delete(d.pending, key)
	d.prune(now)
	name, ttl := pending.name, uint32(300)
	seen := make(map[string]bool)
	for depth := 0; depth < 16; depth++ {
		if seen[name] {
			return
		}
		seen[name] = true
		next := ""
		for _, answer := range msg.Answers {
			if answer.Header.Class != dnsmessage.ClassINET || dnsName(answer.Header.Name) != name {
				continue
			}
			if cname, ok := answer.Body.(*dnsmessage.CNAMEResource); ok {
				candidate := dnsName(cname.CNAME)
				if next != "" && next != candidate {
					return
				}
				next, ttl = candidate, min(ttl, answer.Header.TTL)
			}
		}
		if next != "" {
			name = next
			continue
		}
		for _, answer := range msg.Answers {
			if answer.Header.Class != dnsmessage.ClassINET || dnsName(answer.Header.Name) != name || answer.Header.Type != pending.kind {
				continue
			}
			var ip netip.Addr
			switch body := answer.Body.(type) {
			case *dnsmessage.AResource:
				ip = netip.AddrFrom4(body.A)
			case *dnsmessage.AAAAResource:
				ip = netip.AddrFrom16(body.AAAA).Unmap()
			}
			lifetime := min(ttl, answer.Header.TTL)
			if ip.IsValid() && lifetime > 0 {
				d.remember(ip, pending.name, now.Add(time.Duration(lifetime)*time.Second))
			}
		}
		return
	}
}

// DNS over TCP uses a two-byte length prefix (RFC 7766). Only complete,
// length-bounded frames entirely contained in the current TCP segment are
// observed. Partial/out-of-order segments deliberately remain unattributed:
// snooping must never alter, delay or reassemble the live DNS connection.
func forEachCompleteDNSTCPFrame(payload []byte, observe func([]byte)) {
	for len(payload) >= 2 {
		size := int(binary.BigEndian.Uint16(payload[:2]))
		if size < 12 || size > 8192 || len(payload)-2 < size {
			return
		}
		observe(payload[2 : 2+size])
		payload = payload[2+size:]
	}
}

func (d *dnsAssociations) queryTCP(client, server netip.AddrPort, payload []byte) {
	if server.Port() != 53 {
		return
	}
	forEachCompleteDNSTCPFrame(payload, func(frame []byte) {
		d.queryProtocol(ProtoTCP, client, server, frame)
	})
}

func (d *dnsAssociations) responseTCP(server, client netip.AddrPort, payload []byte) {
	if server.Port() != 53 {
		return
	}
	forEachCompleteDNSTCPFrame(payload, func(frame []byte) {
		d.responseProtocol(ProtoTCP, server, client, frame)
	})
}

func (d *dnsAssociations) remember(ip netip.Addr, name string, expires time.Time) {
	entry := d.addresses[ip]
	if entry == nil {
		if len(d.addresses) >= dnsCapacity {
			return
		}
		entry = &dnsNames{names: make(map[string]time.Time)}
		d.addresses[ip] = entry
	}
	if _, exists := entry.names[name]; !exists && len(entry.names) >= 8 {
		if expires.After(entry.ambiguousUntil) {
			entry.ambiguousUntil = expires
		}
		return
	}
	entry.names[name] = expires
}

func (d *dnsAssociations) lookup(ip netip.Addr) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := d.addresses[ip.Unmap()]
	if entry == nil {
		return ""
	}
	now := d.now()
	if entry.ambiguousUntil.After(now) {
		return ""
	}
	name := ""
	for candidate, expires := range entry.names {
		if !expires.After(now) {
			delete(entry.names, candidate)
			continue
		}
		if name != "" {
			return ""
		}
		name = candidate
	}
	return name
}

func (d *dnsAssociations) prune(now time.Time) {
	for key, query := range d.pending {
		if !query.expires.After(now) {
			delete(d.pending, key)
		}
	}
	for ip, entry := range d.addresses {
		for name, expires := range entry.names {
			if !expires.After(now) {
				delete(entry.names, name)
			}
		}
		if len(entry.names) == 0 && !entry.ambiguousUntil.After(now) {
			delete(d.addresses, ip)
		}
	}
}
