package bridge

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/agent/exit"
)

const (
	customExitProbeDNSHost    = "9.9.9.9"
	customExitProbeDNSTimeout = 3 * time.Second
)

// probeCustomExitUDP checks actual UDP data transfer through the SOCKS5
// association, not just the TCP UDP ASSOCIATE handshake. This runs only on
// an explicit user request. The test DNS name is sent to Quad9 *through*
// the chosen SOCKS5 server, never through the system resolver.
func probeCustomExitUDP(ctx context.Context, upstream exit.UpstreamConfig) (time.Duration, error) {
	name, err := dnsmessage.NewName("example.com.")
	if err != nil {
		return 0, err
	}
	question := dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 0x5237, RecursionDesired: true},
		Questions: []dnsmessage.Question{question},
	}
	wire, err := query.Pack()
	if err != nil {
		return 0, err
	}
	start := time.Now()
	pc, err := exit.DialViaUpstreamUDP(ctx, upstream, customExitProbeDNSHost, 53)
	if err != nil {
		return 0, fmt.Errorf("SOCKS5 UDP ASSOCIATE failed: %w", err)
	}
	defer pc.Close()
	deadline := time.Now().Add(customExitProbeDNSTimeout)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	if err := pc.SetDeadline(deadline); err != nil {
		return 0, err
	}
	if _, err := pc.WriteTo(wire, nil); err != nil {
		return 0, fmt.Errorf("SOCKS5 UDP send failed: %w", err)
	}
	answer := make([]byte, 4096)
	n, _, err := pc.ReadFrom(answer)
	if err != nil {
		return 0, fmt.Errorf("SOCKS5 UDP response not received: %w", err)
	}
	var response dnsmessage.Message
	if err := response.Unpack(answer[:n]); err != nil ||
		!response.Response || response.ID != query.ID ||
		len(response.Questions) != 1 || response.Questions[0] != question {
		return 0, fmt.Errorf("SOCKS5 UDP DNS probe returned an invalid response")
	}
	return time.Since(start), nil
}
