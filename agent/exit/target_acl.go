package exit

import (
	"context"
	"fmt"
	"net"
	"slices"

	"relayproxy/internal/acl"
)

// requestACL requires both the Exit's policy and the policy bound by its
// authenticated Relay to allow the original host and every resolved address.
type requestACL struct {
	local *acl.Checker
	relay *acl.Checker
}

func (h *Handler) aclForRequest(policy *acl.Policy) (*requestACL, error) {
	if h.cfg.ACLChecker == nil && policy == nil {
		return nil, nil
	}
	checker := &requestACL{local: h.cfg.ACLChecker}
	if policy != nil {
		relay, err := h.compiledRelayACL(policy)
		if err != nil {
			return nil, fmt.Errorf("invalid relay ACL policy: %w", err)
		}
		checker.relay = relay
	}
	return checker, nil
}

func (h *Handler) compiledRelayACL(policy *acl.Policy) (*acl.Checker, error) {
	if policy == nil {
		return nil, nil
	}
	key := policy.Fingerprint
	if key == "" {
		// Compatibility path for focused tests and older peers that don't carry
		// the compiled-policy fingerprint. Correctness is unchanged; only the
		// cache fast path is unavailable.
		return acl.NewChecker(*policy)
	}

	h.relayACLMu.Lock()
	defer h.relayACLMu.Unlock()
	if h.relayACLCacheKey == key && h.relayACLCache != nil {
		return h.relayACLCache, nil
	}
	compiled, err := acl.NewChecker(*policy)
	if err != nil {
		return nil, err
	}
	verified := compiled.Policy()
	if verified.Fingerprint != key {
		return nil, fmt.Errorf("relay ACL fingerprint mismatch")
	}
	h.relayACLCacheKey = key
	h.relayACLCache = compiled
	return compiled, nil
}

func (a *requestACL) CheckHostProtocol(ctx context.Context, host string, port uint16, transport string) error {
	if a.local != nil {
		if err := a.local.CheckHostProtocol(ctx, host, port, transport); err != nil {
			return fmt.Errorf("exit ACL: %w", err)
		}
	}
	if a.relay != nil {
		if err := a.relay.CheckHostProtocol(ctx, host, port, transport); err != nil {
			return fmt.Errorf("relay ACL: %w", err)
		}
	}
	return nil
}

func (a *requestACL) CheckIPProtocol(ctx context.Context, ip net.IP, port uint16, transport string) error {
	if a.local != nil {
		if err := a.local.CheckIPProtocol(ctx, ip, port, transport); err != nil {
			return fmt.Errorf("exit ACL: %w", err)
		}
	}
	if a.relay != nil {
		if err := a.relay.CheckIPProtocol(ctx, ip, port, transport); err != nil {
			return fmt.Errorf("relay ACL: %w", err)
		}
	}
	return nil
}

type relayPolicyContextKey struct{}

// BindRelayPolicy binds the server-authoritative Relay ACL to a direct P2P
// stream. The Exit handler always prefers this trusted context value over any
// RelayPolicy supplied by the peer in OpenTCP/OpenUDP.
func BindRelayPolicy(ctx context.Context, policy *acl.Policy) context.Context {
	if policy == nil {
		return ctx
	}
	copy := *policy
	// Preserve nil-vs-empty slice representation exactly. The relay ACL
	// fingerprint is computed from the serialized policy, where nil encodes as
	// null and an empty non-nil slice encodes as []. append(nil, empty...) would
	// collapse [] into nil and make an unchanged authenticated policy fail its
	// own fingerprint verification on the server-local exit path.
	copy.Rules = slices.Clone(policy.Rules)
	copy.AccessHosts = slices.Clone(policy.AccessHosts)
	copy.AccessCIDRs = slices.Clone(policy.AccessCIDRs)
	return context.WithValue(ctx, relayPolicyContextKey{}, &copy)
}

func boundRelayPolicy(ctx context.Context) *acl.Policy {
	if ctx == nil {
		return nil
	}
	policy, _ := ctx.Value(relayPolicyContextKey{}).(*acl.Policy)
	if policy == nil {
		return nil
	}
	copy := *policy
	copy.Rules = slices.Clone(policy.Rules)
	copy.AccessHosts = slices.Clone(policy.AccessHosts)
	copy.AccessCIDRs = slices.Clone(policy.AccessCIDRs)
	return &copy
}
