package gateway

import (
	"context"
	"sort"
	"sync"
	"slices"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

// Only an authenticated and approved files.host can enumerate prospective
// files.client devices for the folder ACL picker. Offline approved peers are
// shown so permissions can be preconfigured safely by device name.
func (r *StreamRouter) handleFileShareCandidates(stream tunnel.TunnelStream, host *session.DeviceSession) {
	resp := protocol.FileShareCandidatesResponse{Devices: []protocol.FileShareCandidate{}}
	if r.fileShareCandidates == nil || !slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
		resp.Error = "files.host is not approved"
		_ = protocol.WriteJSON(stream, resp)
		return
	}
	clients, err := r.fileShareCandidates(host.DeviceID)
	if err != nil {
		resp.Error = "unable to query approved file clients"
		_ = protocol.WriteJSON(stream, resp)
		return
	}
	for _, candidate := range clients {
		peer, connected := r.sessions.Get(candidate.DeviceID)
		candidate.Online = connected && peer != nil && peer.IdentityID == host.IdentityID &&
			slices.Contains(peer.Grants, protocol.CapabilityFilesClient)
		resp.Devices = append(resp.Devices, candidate)
	}
	_ = protocol.WriteJSON(stream, resp)
}

// Device discovery is gated by BOTH Server file grants and the host's
// current local folder ACL. A host with files.host but no active share for
// this client must not appear in the device inventory at all.
// Fail closed on old/unreachable hosts that cannot answer the new query.
func (r *StreamRouter) handleFileSharePeers(ctx context.Context, stream tunnel.TunnelStream, client *session.DeviceSession) {
	response := protocol.FileSharePeersResponse{Peers: []protocol.FileSharePeer{}}
	if r.fileShareChecker == nil ||
		!slices.Contains(client.Grants, protocol.CapabilityFilesClient) {
		response.Error = "file sharing client capability is not approved"
		_ = protocol.WriteJSON(stream, response)
		return
	}
	// Do not let a stalled host hold device discovery indefinitely.
	queryCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, host := range r.sessions.List() {
		if host == nil || host.Tunnel == nil || host.DeviceID == client.DeviceID ||
			host.IdentityID == "" || host.IdentityID != client.IdentityID ||
			!slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-queryCtx.Done():
			break
		}
		if queryCtx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(host *session.DeviceSession) {
			defer wg.Done()
			defer func() { <-sem }()
			allowed, err := r.fileShareChecker(client.DeviceID, host.DeviceID)
			if err != nil || !allowed || !hostHasVisibleFileShare(queryCtx, host, client.DeviceID) {
				return
			}
			// Recheck current session and approval after the host's ACL answer.
			current, online := r.sessions.Get(host.DeviceID)
			allowed, err = r.fileShareChecker(client.DeviceID, host.DeviceID)
			if !online || current != host || err != nil || !allowed {
				return
			}
			mu.Lock()
			response.Peers = append(response.Peers, protocol.FileSharePeer{
				DeviceID: host.DeviceID, Name: host.DeviceName, Online: true,
			})
			mu.Unlock()
		}(host)
	}
	wg.Wait()
	sort.Slice(response.Peers, func(i, j int) bool {
		return response.Peers[i].DeviceID < response.Peers[j].DeviceID
	})
	_ = protocol.WriteJSON(stream, response)
}

// Ask the authenticated host for one boolean only; never sync its path/ACL
// inventory to the Server. The host does not trust the querying client to
// provide or forge another identity.
func hostHasVisibleFileShare(ctx context.Context, host *session.DeviceSession, clientID string) bool {
	stream, err := host.Tunnel.OpenStream(ctx)
	if err != nil {
		return false
	}
	defer stream.Close()
	deadline := time.Now().Add(2 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = stream.SetDeadline(deadline)
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Type: protocol.FrameTypeFileShareVisibility, ClientDeviceID: clientID,
		ExitDeviceID: host.DeviceID,
	}); err != nil {
		return false
	}
	var response protocol.FileShareVisibilityResponse
	return protocol.ReadJSON(stream, &response) == nil && response.Allowed
}

func (r *StreamRouter) handleFileShare(
	ctx context.Context,
	header *protocol.StreamHeader,
	clientStream tunnel.TunnelStream,
	client *session.DeviceSession,
) {
	deny := func(reason string) {
		_ = protocol.WriteJSON(clientStream, protocol.FileShareResponse{Success: false, Error: reason})
	}
	if r.fileShareChecker == nil || !slices.Contains(client.Grants, protocol.CapabilityFilesClient) {
		deny("file sharing client capability is not approved")
		return
	}
	hostID := header.ExitDeviceID
	if hostID == "" || hostID == client.DeviceID {
		deny("invalid file sharing target")
		return
	}
	host, ok := r.sessions.Get(hostID)
	if !ok || host == nil || host.Tunnel == nil ||
		host.IdentityID == "" || host.IdentityID != client.IdentityID ||
		!slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
		deny("file sharing target is unavailable")
		return
	}
	allowed, err := r.fileShareChecker(client.DeviceID, hostID)
	if err != nil || !allowed {
		deny("file sharing access denied")
		return
	}
	openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	hostStream, err := host.Tunnel.OpenStream(openCtx)
	if err != nil {
		deny("unable to contact file sharing target")
		return
	}
	defer hostStream.Close()
	// The client identity in this header was replaced with the authenticated
	// session device ID before dispatch; never trust the incoming client value.
	if err := protocol.WriteStreamHeader(hostStream, header); err != nil {
		deny("unable to connect file sharing target")
		return
	}
	_ = clientStream.SetDeadline(time.Time{})
	_ = hostStream.SetDeadline(time.Time{})
	// Revoked grants and replaced authenticated sessions must terminate an
	// active download too, not just block the next opened stream.
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				currentClient, clientOK := r.sessions.Get(client.DeviceID)
				currentHost, hostOK := r.sessions.Get(host.DeviceID)
				stillAllowed, checkErr := r.fileShareChecker(client.DeviceID, host.DeviceID)
				if checkErr != nil || !stillAllowed || !clientOK || !hostOK ||
					currentClient != client || currentHost != host {
					_ = clientStream.Close()
					_ = hostStream.Close()
					return
				}
			}
		}
	}()
	client.ActiveStreams.Add(1)
	host.ActiveStreams.Add(1)
	defer client.ActiveStreams.Add(-1)
	defer host.ActiveStreams.Add(-1)
	r.pipeStreams(ctx, clientStream, hostStream, client, host)
}
