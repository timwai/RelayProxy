package gateway

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"slices"
	"time"

	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

// A direct path is authorized independently from proxy/RDP privileges. The
// Server issues a one-use token to the authenticated host over its Relay
// session, then returns the host's TLS fingerprint to the requesting client.
func (r *StreamRouter) handleFileDirectPrepare(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, client *session.DeviceSession) {
	r.handleFileDirectPrepareMode(ctx, header, clientStream, client, false)
}

func (r *StreamRouter) handleFileDirectPunchPrepare(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, client *session.DeviceSession) {
	r.handleFileDirectPrepareMode(ctx, header, clientStream, client, true)
}

func (r *StreamRouter) handleFileDirectPrepareMode(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, client *session.DeviceSession, punched bool) {
	fail := func(reason string) { _ = protocol.WriteJSON(clientStream, protocol.FileDirectOffer{Error: reason}) }
	if r.fileShareChecker == nil || !slices.Contains(client.Grants, protocol.CapabilityFilesClient) {
		fail("file direct access not approved")
		return
	}
	var clientCandidates []protocol.P2PCandidate
	if punched {
		var request protocol.FileDirectPunchPrepare
		if err := protocol.ReadJSON(clientStream, &request); err != nil {
			fail("invalid file punch offer")
			return
		}
		var err error
		clientCandidates, err = candidate.Validate(request.Candidates)
		if err != nil || len(clientCandidates) == 0 {
			fail("invalid file punch candidates")
			return
		}
	}
	targetID := header.ExitDeviceID
	if targetID == "" || targetID == client.DeviceID {
		fail("invalid file sharing target")
		return
	}
	host, ok := r.sessions.Get(targetID)
	if !ok || host == nil || host.Tunnel == nil || host.IdentityID == "" ||
		host.IdentityID != client.IdentityID ||
		!slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
		fail("file sharing target unavailable")
		return
	}
	allowed, err := r.fileShareChecker(client.DeviceID, targetID)
	if err != nil || !allowed {
		fail("file direct access denied")
		return
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		fail("unable to issue direct ticket")
		return
	}
	offerToken := hex.EncodeToString(token[:])
	deadline := time.Now().Add(20 * time.Second).UnixMilli()
	var punchID uint64
	var punchKey []byte
	if punched {
		var id [8]byte
		var key [32]byte
		if _, err := rand.Read(id[:]); err != nil {
			fail("unable to issue file punch session")
			return
		}
		if _, err := rand.Read(key[:]); err != nil {
			fail("unable to issue file punch secret")
			return
		}
		punchID = binary.BigEndian.Uint64(id[:])
		if punchID == 0 {
			punchID = 1
		}
		punchKey = key[:]
	}
	ticketCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	hostStream, err := host.Tunnel.OpenStream(ticketCtx)
	if err != nil {
		fail("direct target not reachable")
		return
	}
	defer hostStream.Close()
	_ = hostStream.SetDeadline(time.Now().Add(4 * time.Second))
	if err := protocol.WriteStreamHeader(hostStream, &protocol.StreamHeader{
		Type:           protocol.FrameTypeFileDirectGrant,
		ClientDeviceID: client.DeviceID,
		ExitDeviceID:   targetID,
	}); err != nil {
		fail("direct target unavailable")
		return
	}
	if err := protocol.WriteJSON(hostStream, protocol.FileDirectGrant{
		Token: offerToken, ExpiresAt: deadline,
		PunchSessionID: punchID, PunchKey: punchKey, PeerCandidates: clientCandidates,
	}); err != nil {
		fail("direct grant unavailable")
		return
	}
	var response protocol.FileDirectOffer
	if err := protocol.ReadJSON(hostStream, &response); err != nil || response.Error != "" {
		fail("direct host declined offer")
		return
	}
	if len(response.Endpoints) == 0 || len(response.Endpoints) > 16 ||
		len(response.UDPEndpoints) > 16 ||
		len(response.Fingerprint) != 64 {
		fail("invalid file host endpoints")
		return
	}
	for _, ep := range response.Endpoints {
		addr, err := netip.ParseAddrPort(ep)
		if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() ||
			addr.Addr().IsMulticast() || addr.Addr().IsLinkLocalUnicast() {
			fail("invalid direct address")
			return
		}
	}
	for _, ep := range response.UDPEndpoints {
		addr, err := netip.ParseAddrPort(ep)
		if err != nil || addr.Port() == 0 || addr.Addr().IsUnspecified() ||
			addr.Addr().IsMulticast() || addr.Addr().IsLinkLocalUnicast() {
			fail("invalid direct UDP address")
			return
		}
	}
	if punched && len(response.PunchCandidates) > 0 {
		validated, err := candidate.Validate(response.PunchCandidates)
		if err != nil {
			fail("invalid file host punch candidates")
			return
		}
		response.PunchCandidates = validated
		response.PunchSessionID = punchID
		response.PunchKey = punchKey
	}
	if allowed, err := r.fileShareChecker(client.DeviceID, targetID); err != nil || !allowed {
		fail("file access revoked")
		return
	}
	response.Token = offerToken
	response.ExpiresAt = deadline
	_ = protocol.WriteJSON(clientStream, response)
}

// The host must revalidate every live direct file stream against the current
// database grants; Server/identity revocation or loss of the control plane
// closes the direct stream, in addition to Agent-local directory ACL checks.
func (r *StreamRouter) handleFileDirectCheck(stream tunnel.TunnelStream, header *protocol.StreamHeader, host *session.DeviceSession) {
	resp := protocol.FileDirectAuthResult{}
	if r.fileShareChecker != nil && slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
		client, ok := r.sessions.Get(header.ExitDeviceID)
		if ok && client != nil && client.IdentityID != "" &&
			client.IdentityID == host.IdentityID &&
			slices.Contains(client.Grants, protocol.CapabilityFilesClient) {
			resp.Allowed, _ = r.fileShareChecker(client.DeviceID, host.DeviceID)
		}
	}
	_ = protocol.WriteJSON(stream, resp)
}
