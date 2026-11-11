package divert

import (
	"net/netip"

	"golang.org/x/net/dns/dnsmessage"
)

const dnsHostOverrideTTL = 60

func (s *Server) dnsHostOverride(host string) (netip.Addr, bool) {
	if s == nil || s.opts.DNSHostIP == nil {
		return netip.Addr{}, false
	}
	raw, ok := s.opts.DNSHostIP(dnsNameFromString(host))
	if !ok {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func (s *Server) dnsHostOverrideQuestion(raw []byte) (string, netip.Addr, bool) {
	if s == nil {
		return "", netip.Addr{}, false
	}
	var query dnsmessage.Message
	if err := query.Unpack(raw); err != nil || query.Response || query.OpCode != 0 ||
		query.Truncated || len(query.Questions) != 1 || query.Questions[0].Class != dnsmessage.ClassINET ||
		(query.Questions[0].Type != dnsmessage.TypeA && query.Questions[0].Type != dnsmessage.TypeAAAA) {
		return "", netip.Addr{}, false
	}
	host := dnsName(query.Questions[0].Name)
	if host == "" || isRelayDNSQuestion(raw, s.loopGuard().RelayHost) {
		return "", netip.Addr{}, false
	}
	ip, ok := s.dnsHostOverride(host)
	return host, ip, ok
}

func (s *Server) hasDNSHostOverride(raw []byte) bool {
	_, _, ok := s.dnsHostOverrideQuestion(raw)
	return ok
}

// dnsHostOverrideReply returns a deterministic local answer for an exact
// configured hostname. A query for the other address family receives NODATA.
func (s *Server) dnsHostOverrideReply(raw []byte) []byte {
	_, ip, ok := s.dnsHostOverrideQuestion(raw)
	if !ok {
		return nil
	}
	var query dnsmessage.Message
	if err := query.Unpack(raw); err != nil || len(query.Questions) != 1 {
		return nil
	}
	question := query.Questions[0]
	reply := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID: query.ID, Response: true, RecursionDesired: query.RecursionDesired,
			RecursionAvailable: true, RCode: dnsmessage.RCodeSuccess,
		},
		Questions: query.Questions,
	}
	header := dnsmessage.ResourceHeader{
		Name: question.Name, Class: dnsmessage.ClassINET, Type: question.Type,
		TTL: dnsHostOverrideTTL,
	}
	switch {
	case question.Type == dnsmessage.TypeA && ip.Is4():
		reply.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AResource{A: ip.As4()}}}
	case question.Type == dnsmessage.TypeAAAA && ip.Is6():
		reply.Answers = []dnsmessage.Resource{{Header: header, Body: &dnsmessage.AAAAResource{AAAA: ip.As16()}}}
	}
	return packDNSResponse(reply)
}
