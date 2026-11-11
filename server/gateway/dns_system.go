package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

// handleExitSystemDNS allows exactly one bounded DNS A/AAAA lookup on an
// approved proxy exit. The relay itself authenticates the client, authorizes
// the exit and replaces any caller-supplied ACL; it never blindly pipes DNS.
func (r *StreamRouter) handleExitSystemDNS(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.ExitDNSRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.ExitDNSResponse{ErrorCode: protocol.ErrCodeInvalidRequest})
		return
	}
	fail := func(code, message string) {
		_ = protocol.WriteJSON(clientStream, protocol.ExitDNSResponse{
			RequestID: req.RequestID, ErrorCode: code, ErrorMessage: message,
		})
	}
	if !containsCapability(clientSession.Grants, protocol.CapabilityProxyClient) {
		fail(protocol.ErrCodeAccessDenied, "proxy.client approval required")
		return
	}
	if req.RequestID == "" || len(req.RequestID) > protocol.MaxStringLen {
		fail(protocol.ErrCodeInvalidRequest, "invalid request identifier")
		return
	}
	_, hostname, err := protocol.ValidateExitDNSQuestion(req.Question)
	if err != nil {
		fail(protocol.ErrCodeInvalidRequest, err.Error())
		return
	}
	exitSession, localExit, err := r.resolveExitSession(clientSession, header.ExitDeviceID)
	if err != nil {
		fail(protocol.ErrCodeExitOffline, err.Error())
		return
	}
	if !localExit {
		allowed, authErr := r.authorizeExit(clientSession, exitSession)
		if authErr != nil || !allowed {
			fail(protocol.ErrCodeAccessDenied, "client is not authorized for this exit")
			return
		}
	}
	// A forged client policy is discarded unconditionally.
	req.RelayPolicy = nil
	if localExit {
		if r.localExit == nil {
			fail(protocol.ErrCodeExitOffline, "server exit unavailable")
			return
		}
		response, dnsErr := r.localExit.ResolveSystemDNS(exit.BindRelayPolicy(ctx, r.relayPolicy), req)
		if dnsErr != nil {
			failExitSystemDNS(clientStream, req.RequestID, dnsErr)
			return
		}
		_ = protocol.WriteJSON(clientStream, response)
		return
	}
	if !hasCapability(exitSession, protocol.CapabilityExitSystemDNS) {
		fail(protocol.ErrCodeDNSUnsupported, fmt.Sprintf("出口 %s 未声明支持系统 DNS，请升级该出口 Agent 并重新连接", exitSession.DeviceID))
		return
	}
	if !hasCapability(exitSession, protocol.CapabilityTargetACL) {
		fail(protocol.ErrCodeACLDenied, "selected exit does not enforce the Relay ACL")
		return
	}
	// Only modern exits that enforce the Relay ACL are permitted. This
	// prevents legacy or malicious paths from becoming unfiltered DNS oracles.
	policy, err := r.targetPolicy(ctx, exitSession, hostname, 53, "udp")
	if err != nil {
		fail(protocol.ErrCodeACLDenied, err.Error())
		return
	}
	req.RelayPolicy = policy
	openCtx, cancel := context.WithDeadline(ctx, handshakeDeadline)
	defer cancel()
	exitStream, err := exitSession.Tunnel.OpenStream(openCtx)
	if err != nil {
		fail(protocol.ErrCodeStreamOpenFailed, "exit stream unavailable")
		return
	}
	defer exitStream.Close()
	stop := tunnel.InterruptOnCancel(ctx, exitStream)
	defer stop()
	_ = exitStream.SetDeadline(handshakeDeadline)
	header.ExitDeviceID = exitSession.DeviceID
	if err = protocol.WriteStreamHeader(exitStream, header); err == nil {
		err = protocol.WriteJSON(exitStream, req)
	}
	if err != nil {
		fail(protocol.ErrCodeStreamOpenFailed, "unable to send exit DNS request")
		return
	}
	var response protocol.ExitDNSResponse
	if err := protocol.ReadJSON(exitStream, &response); err != nil {
		fail(protocol.ErrCodeDNSFailed, "exit DNS reply unavailable")
		return
	}
	if response.RequestID != req.RequestID {
		fail(protocol.ErrCodeDNSFailed, "exit DNS reply identifier mismatch")
		return
	}
	if response.Success {
		ips, err := protocol.ValidateExitDNSAnswer(req.Question, response.Answer)
		if err != nil {
			fail(protocol.ErrCodeDNSFailed, "exit DNS reply is invalid")
			return
		}
		// Check the returned addresses at the Relay too, rather than solely
		// trusting a device to enforce the signed central policy.
		if r.aclChecker != nil {
			for _, addr := range ips {
				if err := r.aclChecker.CheckIPProtocol(ctx, net.IP(addr), 53, "udp"); err != nil {
					fail(protocol.ErrCodeACLDenied, "exit DNS result denied by Relay ACL")
					return
				}
			}
		}
	} else if len(response.Answer) != 0 {
		fail(protocol.ErrCodeDNSFailed, "unexpected failed DNS reply payload")
		return
	}
	_ = protocol.WriteJSON(clientStream, response)
}

func failExitSystemDNS(stream tunnel.TunnelStream, requestID string, err error) {
	var relayErr *protocol.RelayError
	code, message := protocol.ErrCodeDNSFailed, "exit system DNS failed"
	if errors.As(err, &relayErr) {
		code, message = relayErr.Code, relayErr.Message
	}
	_ = protocol.WriteJSON(stream, protocol.ExitDNSResponse{
		RequestID: requestID, ErrorCode: code, ErrorMessage: message,
	})
}
