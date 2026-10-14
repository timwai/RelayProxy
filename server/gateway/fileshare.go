package gateway

import (
	"context"
	"slices"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

// Device discovery returns authenticated, online peers only. It never leaks
// directory names or local paths, and is refreshed on demand.
func (r *StreamRouter) handleFileSharePeers(stream tunnel.TunnelStream, client *session.DeviceSession) {
	response := protocol.FileSharePeersResponse{Peers: []protocol.FileSharePeer{}}
	if r.fileShareChecker == nil ||
		!slices.Contains(client.Grants, protocol.CapabilityFilesClient) {
		response.Error = "file sharing client capability is not approved"
		_ = protocol.WriteJSON(stream, response)
		return
	}
	for _, host := range r.sessions.List() {
		if host == nil || host.DeviceID == client.DeviceID ||
			host.IdentityID == "" || host.IdentityID != client.IdentityID ||
			!slices.Contains(host.Grants, protocol.CapabilityFilesHost) {
			continue
		}
		allowed, err := r.fileShareChecker(client.DeviceID, host.DeviceID)
		if err == nil && allowed {
			response.Peers = append(response.Peers, protocol.FileSharePeer{
				DeviceID: host.DeviceID, Name: host.DeviceName, Online: true,
			})
		}
	}
	_ = protocol.WriteJSON(stream, response)
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
	client.ActiveStreams.Add(1)
	host.ActiveStreams.Add(1)
	defer client.ActiveStreams.Add(-1)
	defer host.ActiveStreams.Add(-1)
	r.pipeStreams(ctx, clientStream, hostStream, client, host)
}
