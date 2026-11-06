package exit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// SystemDNSLookup uses the selected Exit's operating-system resolver. The
// handler never listens on UDP/53 and cannot be used as a generic DNS forwarder.
type SystemDNSLookup func(context.Context, string, string) ([]net.IP, error)

const systemDNSLookupTimeout = 7 * time.Second

// ResolveSystemDNS is shared by local server exits and remote agent exits.
// An authenticated relay policy is supplied through context for the former
// and overwritten into the request for the latter.
func (h *Handler) ResolveSystemDNS(ctx context.Context, req protocol.ExitDNSRequest) (protocol.ExitDNSResponse, error) {
	answer := protocol.ExitDNSResponse{RequestID: req.RequestID}
	if req.RequestID == "" || len(req.RequestID) > protocol.MaxStringLen {
		return answer, protocol.NewRelayError(protocol.ErrCodeInvalidRequest, "invalid DNS request identifier")
	}
	// If this Exit's own transparent DNS/53 capture is active, the OS
	// resolver may reenter this Agent and recursively trigger another DNS
	// request. Reject safely until a platform-specific bypass is available.
	if h.cfg.SystemDNSAllowed != nil && !h.cfg.SystemDNSAllowed() {
		return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, "exit system DNS unavailable while DNS interception is active")
	}
	msg, host, err := protocol.ValidateExitDNSQuestion(req.Question)
	if err != nil {
		return answer, protocol.NewRelayError(protocol.ErrCodeInvalidRequest, err.Error())
	}
	if policy := boundRelayPolicy(ctx); policy != nil {
		req.RelayPolicy = policy
	}
	checker, err := h.aclForRequest(req.RelayPolicy)
	if err != nil {
		return answer, protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
	}
	// The same exit and relay ACLs that gate regular proxy traffic protect
	// both the original hostname and every returned IP. No hidden private-IP
	// disclosures through a public domain that resolves to RFC1918 addresses.
	if checker != nil {
		if err := checker.CheckHostProtocol(ctx, host, 53, "udp"); err != nil {
			return answer, protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
		}
	}
	ctx, cancel := context.WithTimeout(ctx, systemDNSLookupTimeout)
	defer cancel()
	network := "ip4"
	if msg.Questions[0].Type == dnsmessage.TypeAAAA {
		network = "ip6"
	}
	lookup := h.cfg.SystemDNSLookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIP
	}
	ips, err := lookup(ctx, network, host)
	if err != nil {
		// A/AAAA negative results cannot always distinguish NODATA from
		// NXDOMAIN through net.Resolver. Return a validated empty answer
		// instead of claiming the entire domain is nonexistent.
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, "exit system resolver failed")
		}
		ips = nil
	}
	if err := ctx.Err(); err != nil {
		return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, err.Error())
	}
	if len(ips) > protocol.MaxExitDNSAnswers {
		ips = ips[:protocol.MaxExitDNSAnswers]
	}
	resources := make([]dnsmessage.Resource, 0, len(ips))
	q := msg.Questions[0]
	for _, ip := range ips {
		if ip == nil {
			return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, "invalid resolved address")
		}
		// Check the full lookup result before releasing any address.
		if checker != nil {
			if err := checker.CheckIPProtocol(ctx, ip, 53, "udp"); err != nil {
				return answer, protocol.NewRelayError(protocol.ErrCodeACLDenied, err.Error())
			}
		}
		header := dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: protocol.ExitDNSTTLSeconds}
		switch q.Type {
		case dnsmessage.TypeA:
			ip4 := ip.To4()
			if ip4 == nil {
				return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, "invalid IPv4 result")
			}
			var v4 [4]byte
			copy(v4[:], ip4)
			resources = append(resources, dnsmessage.Resource{Header: header, Body: &dnsmessage.AResource{A: v4}})
		case dnsmessage.TypeAAAA:
			if ip.To4() != nil || ip.To16() == nil {
				return answer, protocol.NewRelayError(protocol.ErrCodeDNSFailed, "invalid IPv6 result")
			}
			var v6 [16]byte
			copy(v6[:], ip.To16())
			resources = append(resources, dnsmessage.Resource{Header: header, Body: &dnsmessage.AAAAResource{AAAA: v6}})
		}
	}
	wire, err := (dnsmessage.Message{Header: dnsmessage.Header{
		ID: msg.ID, Response: true, RecursionDesired: msg.RecursionDesired, RecursionAvailable: true,
	}, Questions: msg.Questions, Answers: resources}).Pack()
	if err != nil || len(wire) > protocol.MaxExitDNSAnswerBytes {
		return answer, fmt.Errorf("exit DNS response exceeds limit or cannot be encoded: %w", err)
	}
	answer.Success, answer.Answer = true, wire
	return answer, nil
}

func (h *Handler) handleExitSystemDNS(ctx context.Context, stream tunnel.TunnelStream) {
	var req protocol.ExitDNSRequest
	if err := protocol.ReadJSON(stream, &req); err != nil {
		_ = protocol.WriteJSON(stream, protocol.ExitDNSResponse{ErrorCode: protocol.ErrCodeInvalidRequest})
		return
	}
	resp, err := h.ResolveSystemDNS(ctx, req)
	if err != nil {
		var relay *protocol.RelayError
		if errors.As(err, &relay) {
			resp.ErrorCode, resp.ErrorMessage = relay.Code, relay.Message
		} else {
			resp.ErrorCode = protocol.ErrCodeDNSFailed
			resp.ErrorMessage = "exit system DNS resolution failed"
		}
	}
	_ = protocol.WriteJSON(stream, resp)
}
