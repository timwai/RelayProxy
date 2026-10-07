package direct

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type CurrentTicketAuthorization struct {
	RelayPolicy       *acl.Policy
	BrutalUploadBPS   uint64
	BrutalDownloadBPS uint64
}

type CurrentTicketValidator func(context.Context, string, protocol.PublicDirectTicketValidationRequest) (CurrentTicketAuthorization, error)

type Controller struct {
	ctx             context.Context
	cancel          context.CancelFunc
	registry        *Registry
	verifier        *Verifier
	onChanged       func(string)
	ticketValidator CurrentTicketValidator
	mu              sync.Mutex
	wg              sync.WaitGroup
	closed          atomic.Bool
}

func NewController(parent context.Context, registry *Registry, verifier *Verifier, onChanged func(string)) *Controller {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &Controller{
		ctx: ctx, cancel: cancel, registry: registry, verifier: verifier, onChanged: onChanged,
	}
}

func (c *Controller) SetTicketValidator(validator CurrentTicketValidator) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ticketValidator = validator
	c.mu.Unlock()
}

func (c *Controller) Close() error {
	if c == nil || c.closed.Swap(true) {
		return nil
	}
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
	return nil
}

func (c *Controller) HandleControl(ctx context.Context, stream tunnel.TunnelStream, dev *session.DeviceSession) {
	if stream == nil {
		return
	}
	defer stream.Close()

	if c == nil || c.registry == nil || c.verifier == nil || dev == nil ||
		dev.DeviceID == "" || dev.SessionID == "" || !dev.IsExit() {
		_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
			ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct registration is unavailable",
		})
		return
	}

	var request protocol.PublicDirectRegistrationRequest
	if err := protocol.ReadJSON(stream, &request); err != nil {
		_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
			ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "invalid public direct control request",
		})
		return
	}

	operation := strings.TrimSpace(request.Operation)
	if operation == protocol.PublicDirectControlValidateTicket {
		c.mu.Lock()
		validator := c.ticketValidator
		c.mu.Unlock()
		validation := request.TicketValidation
		if validator == nil || validation == nil ||
			strings.TrimSpace(validation.ClientDeviceID) == "" ||
			strings.TrimSpace(validation.ExitDeviceID) != dev.DeviceID ||
			validation.PolicyRevision <= 0 || validation.AuthorizationRevision <= 0 {
			_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
				ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct authorization is unavailable",
			})
			return
		}
		authorization, err := validator(ctx, dev.DeviceID, *validation)
		if err != nil {
			_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
				ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct authorization is no longer current",
			})
			return
		}
		policy := authorization.RelayPolicy
		if policy == nil || strings.TrimSpace(policy.Fingerprint) == "" {
			_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
				ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct relay policy is unavailable",
			})
			return
		}
		checker, err := acl.NewChecker(*policy)
		if err != nil {
			_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
				ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct relay policy is invalid",
			})
			return
		}
		normalized := checker.Policy()
		if normalized.Fingerprint != policy.Fingerprint {
			_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
				ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: "public direct relay policy fingerprint mismatch",
			})
			return
		}
		_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
			Success: true, RelayPolicy: &normalized,
			BrutalUploadBPS: authorization.BrutalUploadBPS,
			BrutalDownloadBPS: authorization.BrutalDownloadBPS,
		})
		return
	}
	if operation != "" && operation != protocol.PublicDirectControlRegister {
		_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
			ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: "unsupported public direct control operation",
		})
		return
	}

	records, err := c.registry.Register(dev.DeviceID, dev.SessionID, observedAddr(dev.Tunnel), request)
	if err != nil {
		_ = protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
			ErrorCode: protocol.ErrCodeInvalidRequest, ErrorMessage: err.Error(),
		})
		return
	}

	if err := protocol.WriteJSON(stream, protocol.PublicDirectRegistrationResponse{
		Success:   true,
		Endpoints: c.registry.VerifiedEndpoints(dev.DeviceID),
	}); err != nil {
		return
	}

	for _, record := range records {
		if record.State == StateVerifying {
			continue
		}
		// Re-probe even a currently verified endpoint when the authenticated
		// Exit refreshes its registration. This renews the short verification
		// TTL without publishing an unverified address to Clients.
		c.verify(dev.DeviceID, dev.SessionID, record.Endpoint.Address)
	}
}

func (c *Controller) verify(deviceID, sessionID, address string) {
	c.mu.Lock()
	if c.closed.Load() {
		c.mu.Unlock()
		return
	}
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		before := c.registry.VerifiedEndpoints(deviceID)
		err := c.verifier.Verify(c.ctx, deviceID, sessionID, address)
		if errors.Is(err, context.Canceled) {
			return
		}
		after := c.registry.VerifiedEndpoints(deviceID)
		if endpointSetChanged(before, after) && c.onChanged != nil {
			c.onChanged(deviceID)
		}
	}()
}

func (c *Controller) InvalidateDevice(deviceID string) {
	if c == nil || c.registry == nil {
		return
	}
	had := len(c.registry.VerifiedEndpoints(deviceID)) > 0
	c.registry.InvalidateDevice(deviceID)
	if had && c.onChanged != nil {
		c.onChanged(deviceID)
	}
}

func observedAddr(session tunnel.TunnelSession) netip.Addr {
	if session == nil || session.RemoteAddr() == nil {
		return netip.Addr{}
	}
	switch address := session.RemoteAddr().(type) {
	case *net.TCPAddr:
		if value, ok := netip.AddrFromSlice(address.IP); ok {
			return value.Unmap()
		}
	case *net.UDPAddr:
		if value, ok := netip.AddrFromSlice(address.IP); ok {
			return value.Unmap()
		}
	default:
		host, _, err := net.SplitHostPort(session.RemoteAddr().String())
		if err == nil {
			if value, err := netip.ParseAddr(host); err == nil {
				return value.Unmap()
			}
		}
	}
	return netip.Addr{}
}

func endpointSetChanged(left, right []protocol.PublicDirectEndpoint) bool {
	if len(left) != len(right) {
		return true
	}
	seen := make(map[string]struct{}, len(left))
	for _, endpoint := range left {
		seen[endpoint.Protocol+"|"+endpoint.Address+"|"+endpoint.DialAddress+"|"+endpoint.Source+"|"+
			endpoint.CertFingerprint+"|"+fmt.Sprint(endpoint.Verified)] = struct{}{}
	}
	for _, endpoint := range right {
		key := endpoint.Protocol + "|" + endpoint.Address + "|" + endpoint.DialAddress + "|" + endpoint.Source + "|" +
			endpoint.CertFingerprint + "|" + fmt.Sprint(endpoint.Verified)
		if _, ok := seen[key]; !ok {
			return true
		}
	}
	return false
}
