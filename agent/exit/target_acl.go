package exit

import (
	"context"
	"fmt"
	"net"

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
		var err error
		checker.relay, err = acl.NewChecker(*policy)
		if err != nil {
			return nil, fmt.Errorf("invalid relay ACL policy: %w", err)
		}
	}
	return checker, nil
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
