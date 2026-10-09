package divert

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Private benchmarking range (RFC 2544) and documentation-only IPv6 prefix.
// These addresses are placeholders and MUST NEVER be forwarded onto the wire.
// The two address families share a hostname but have distinct addresses.
const (
	fakeIPLimit = 32768
	fakeIPTTL   = 60
)

var (
	fakeIPv4Range = netip.MustParsePrefix("198.18.0.0/15")
	fakeIPv6Range = netip.MustParsePrefix("2001:db8:198:18::/96")
)

type fakeIPEntry struct {
	host    string
	expires time.Time
}

type fakeIPDNS struct {
	mu           sync.Mutex
	now          func() time.Time
	next4, next6 uint32
	byName       map[string]netip.Addr
	byIP         map[netip.Addr]fakeIPEntry
}

func newFakeIPDNS() *fakeIPDNS {
	return &fakeIPDNS{now: time.Now, byName: make(map[string]netip.Addr), byIP: make(map[netip.Addr]fakeIPEntry)}
}

// Ports used by DNS-over-TLS, DNS-over-QUIC and related non-HTTP transports.
// HTTPS-based DoH on port 443 cannot be distinguished at the packet layer.
func isEncryptedDNSPort(port uint16) bool { return port == 853 || port == 784 || port == 8853 }

func isDNSLeakPort(port uint16) bool { return port == 53 || isEncryptedDNSPort(port) }

func isFakeIP(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsValid() && (fakeIPv4Range.Contains(addr) || fakeIPv6Range.Contains(addr))
}

func (d *fakeIPDNS) lookup(addr netip.Addr) (string, bool) {
	if !isFakeIP(addr) {
		return "", false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.byIP[addr.Unmap()]
	if !ok || !entry.expires.After(d.now()) {
		return "", false
	}
	return entry.host, true
}

func (d *fakeIPDNS) allocate(host string, kind dnsmessage.Type) (netip.Addr, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := host + "/" + string(rune(kind))
	now := d.now()
	if existing, ok := d.byName[key]; ok {
		if entry, found := d.byIP[existing]; found && entry.expires.After(now) {
			entry.expires = now.Add(15 * time.Minute)
			d.byIP[existing] = entry
			return existing, true
		}
		delete(d.byName, key)
		delete(d.byIP, existing)
	}
	if len(d.byIP) >= fakeIPLimit {
		// Never evict a live association just to serve a new question; that
		// could redirect the original TCP session to a different domain.
		return netip.Addr{}, false
	}
	var ip netip.Addr
	if kind == dnsmessage.TypeA {
		d.next4++
		if d.next4 > 131070 {
			return netip.Addr{}, false
		}
		id := d.next4
		ip = netip.AddrFrom4([4]byte{198, 18 + byte(id>>16), byte(id >> 8), byte(id)})
	} else {
		d.next6++
		if d.next6 == 0 {
			return netip.Addr{}, false
		}
		raw := netip.MustParseAddr("2001:db8:198:18::").As16()
		raw[12], raw[13], raw[14], raw[15] = byte(d.next6>>24), byte(d.next6>>16), byte(d.next6>>8), byte(d.next6)
		ip = netip.AddrFrom16(raw)
	}
	d.byName[key] = ip
	d.byIP[ip] = fakeIPEntry{host: host, expires: now.Add(15 * time.Minute)}
	return ip, true
}

// reply synthesizes an answer locally instead of sending a DNS request out of
// the machine. Unsupported question types receive REFUSED, rather than being
// silently forwarded to the operating system's configured resolver.
// The Relay bootstrap hostname is answered from known relay IPs, not FakeIP.
func (d *fakeIPDNS) reply(payload []byte, relayHost string, relayIPs []string) []byte {
	var query dnsmessage.Message
	if err := query.Unpack(payload); err != nil || query.Response {
		return nil
	}
	result := dnsmessage.Message{Header: dnsmessage.Header{
		ID: query.ID, Response: true, RecursionDesired: query.RecursionDesired,
		RecursionAvailable: true, RCode: dnsmessage.RCodeRefused,
	}, Questions: query.Questions}
	if query.OpCode != 0 || len(query.Questions) != 1 || query.Truncated {
		return packDNSResponse(result)
	}
	q := query.Questions[0]
	if q.Class != dnsmessage.ClassINET || (q.Type != dnsmessage.TypeA && q.Type != dnsmessage.TypeAAAA) {
		return packDNSResponse(result)
	}
	host := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	if host == "" || len(host) > 253 {
		return packDNSResponse(result)
	}
	var ip netip.Addr
	var ok bool
	if strings.EqualFold(host, strings.TrimSuffix(relayHost, ".")) && relayHost != "" {
		// Bootstrap is the sole DNS exception; it never requires leaking a
		// query to a local recursive resolver.
		for _, raw := range relayIPs {
			addr, err := netip.ParseAddr(raw)
			if err == nil && ((q.Type == dnsmessage.TypeA && addr.Is4()) || (q.Type == dnsmessage.TypeAAAA && addr.Is6())) {
				ip, ok = addr, true
				break
			}
		}
		if !ok {
			result.RCode = dnsmessage.RCodeServerFailure
			return packDNSResponse(result)
		}
	} else {
		ip, ok = d.allocate(host, q.Type)
		if !ok {
			result.RCode = dnsmessage.RCodeServerFailure
			return packDNSResponse(result)
		}
	}
	result.RCode = dnsmessage.RCodeSuccess
	header := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, Type: q.Type, TTL: fakeIPTTL}
	if q.Type == dnsmessage.TypeA {
		result.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AResource{A: ip.As4()}}}
	} else {
		result.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AAAAResource{AAAA: ip.As16()}}}
	}
	return packDNSResponse(result)
}

func packDNSResponse(m dnsmessage.Message) []byte {
	out, err := m.Pack()
	if err != nil {
		return nil
	}
	return out
}
