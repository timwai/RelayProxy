package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relayproxy/agent/fileshare"
	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/server/gateway"
	"relayproxy/server/session"
)

// Tests two independently authenticated Agents and the real Relay transport,
// not mock direct pipes. The two devices run on one CI host; this is NOT a
// public Internet / distinct physical hardware acceptance result.
func TestFileSharingOverTwoAuthenticatedAgentSessions(t *testing.T) {
	for _, transportMode := range []string{"quic_only", "tcp_only"} {
		t.Run(transportMode, func(t *testing.T) {
			certificate, err := cert.EnsureCertificate("", "", "localhost")
			if err != nil {
				t.Fatal(err)
			}
			sessions := session.NewManager()
			router := gateway.NewStreamRouter(sessions, nil, nil, nil)
			allow := true
			router.SetFileShareChecker(func(clientID, hostID string) (bool, error) {
				return allow && clientID == "files-client" && hostID == "files-host", nil
			})
			server := gateway.NewGateway(gateway.GatewayConfig{
				TCPAddr: "127.0.0.1:0", QUICAddr: "127.0.0.1:0",
				TLSConfig:        &tls.Config{Certificates: []tls.Certificate{certificate}},
				ServerInstanceID: "fileshare-integration",
				ResolveIdentity: func(shortID string) (gateway.IdentityAuthorization, error) {
					return gateway.IdentityAuthorization{IdentityID: "file-test-identity", IdentityName: shortID}, nil
				},
				AuthorizeIdentityDevice: func(_ string, hello protocol.DeviceHello, identity gateway.IdentityAuthorization) (gateway.DeviceAuthorization, error) {
					deviceID := "files-client"
					caps := []string{protocol.CapabilityProxyClient, protocol.CapabilityFilesClient}
					for _, c := range hello.RequestedCapabilities {
						if c == protocol.CapabilityProxyExit {
							deviceID = "files-host"
							caps = []string{protocol.CapabilityProxyExit, protocol.CapabilityFilesHost}
						}
					}
					return gateway.DeviceAuthorization{State: "approved", DeviceID: deviceID,
						IdentityID: identity.IdentityID, ApprovedCapabilities: caps}, nil
				},
				RecheckIdentityDevice: func(string, string, string) bool { return true },
			}, sessions, router)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			disabled := false
			base := AgentConfig{
				ServerAddress: "127.0.0.1",
				TCPPort:       server.TCPAddr().(*net.TCPAddr).Port,
				QUICPort:      server.QUICAddr().(*net.UDPAddr).Port,
				InsecureTLS:   true, SOCKS5Enabled: &disabled, HTTPEnabled: &disabled,
				IdentityID: "a1b2c3d4e5f6g7h8", ConnectTimeout: 3 * time.Second,
			}
			shareDir := t.TempDir()
			data := bytes.Repeat([]byte("file-payload-"), 64*1024)
			if err := os.WriteFile(filepath.Join(shareDir, "source.bin"), data, 0600); err != nil {
				t.Fatal(err)
			}
			hostCfg := base
			hostCfg.Mode = "EXIT"
			hostCfg.TransportMode = transportMode
			hostCfg.AllowInternet, hostCfg.AllowPrivateNet, hostCfg.AllowLoopback = true, true, true
			host, err := NewAgent(hostCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer host.Close()
			store, err := fileshare.New(filepath.Join(t.TempDir(), "agent.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			state.Enabled = true
			state.Shares = []fileshare.Share{{
				Name: "shared", RootPath: shareDir, Enabled: true,
				Permissions: map[string]fileshare.Permission{"files-client": fileshare.PermissionReadWrite},
			}}
			state, err = store.Save(state)
			if err != nil {
				t.Fatal(err)
			}
			host.SetFileShareStore(store)
			if err := host.Start(); err != nil {
				t.Fatal(err)
			}
			clientCfg := base
			clientCfg.Mode = "CLIENT"
			clientCfg.TransportMode = transportMode
			client, err := NewAgent(clientCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := client.Start(); err != nil {
				t.Fatal(err)
			}
			waitAgentReady(t, host)
			waitAgentReady(t, client)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			shares, err := client.RemoteFileShares(ctx, "files-host")
			if err != nil || len(shares) != 1 || shares[0].ID != state.Shares[0].ID {
				t.Fatalf("authorized share inventory: %+v err=%v", shares, err)
			}
			entries, err := client.RemoteFileList(ctx, "files-host", shares[0].ID, ".")
			if err != nil || len(entries) != 1 || entries[0].Name != "source.bin" {
				t.Fatalf("remote listing: %+v err=%v", entries, err)
			}
			var output bytes.Buffer
			if _, err := client.DownloadRemoteFile(ctx, "files-host", shares[0].ID, "source.bin", &output, nil); err != nil {
				t.Fatalf("authenticated download: %v", err)
			}
			if !bytes.Equal(output.Bytes(), data) {
				t.Fatal("download contents differ")
			}
			// An authenticated file-only TLS stream must have been negotiated:
			// direct access must not require the proxy/RDP authorization.
			host.mu.RLock()
			directRuntime := host.fileDirect
			host.mu.RUnlock()
			if directRuntime == nil || directRuntime.accepted.Load() == 0 {
				t.Fatal("file direct TLS stream was not authenticated")
			}
			// Exercise the actual transfer-path reporter on the authenticated
			// TLS direct stream rather than assuming a preferred path was used.
			directOutput, err := os.CreateTemp(t.TempDir(), "direct-*")
			if err != nil {
				t.Fatal(err)
			}
			defer directOutput.Close()
			var directPath string
			if _, err := client.DownloadRemoteFileResumingWithPath(ctx, "files-host", shares[0].ID, "source.bin", directOutput, nil, func(p string) {
				directPath = p
			}); err != nil || directPath != "direct_tls" {
				t.Fatalf("expected real TLS direct file transport; path=%q err=%v", directPath, err)
			}
			// A direct listener outage must not break existing Relay
			// downloads: force direct negotiation to fail, then verify that
			// Server-checked Relay forwarding succeeds without data loss.
			directRuntime.close()
			fallbackFile, err := os.CreateTemp(t.TempDir(), "fallback-*")
			if err != nil {
				t.Fatal(err)
			}
			defer fallbackFile.Close()
			var fallbackPath string
			if _, err := client.DownloadRemoteFileResumingWithPath(ctx, "files-host", shares[0].ID, "source.bin", fallbackFile, nil, func(p string) {
				fallbackPath = p
			}); err != nil {
				t.Fatalf("Relay fallback after direct outage: %v", err)
			}
			fallback, err := os.ReadFile(fallbackFile.Name())
			if err != nil || !bytes.Equal(fallback, data) {
				t.Fatalf("Relay fallback data mismatch: %v", err)
			}
			expectedPath := "relay_quic"
			if transportMode == "tcp_only" {
				expectedPath = "relay_tls"
			}
			if fallbackPath != expectedPath {
				t.Fatalf("unexpected fallback transport path %q (want %q)", fallbackPath, expectedPath)
			}
			// Resume after a simulated local connection failure: half a file
			// already on disk is verified byte-for-byte against the host.
			partialFile, err := os.CreateTemp(t.TempDir(), "partial-*")
			if err != nil {
				t.Fatal(err)
			}
			defer partialFile.Close()
			if _, err := partialFile.Write(data[:len(data)/2]); err != nil {
				t.Fatal(err)
			}
			if _, err := client.DownloadRemoteFileResuming(ctx, "files-host", shares[0].ID, "source.bin", partialFile, nil); err != nil {
				t.Fatalf("authenticated download resume: %v", err)
			}
			result, err := os.ReadFile(partialFile.Name())
			if err != nil || !bytes.Equal(result, data) {
				t.Fatalf("resumed data invalid: %v", err)
			}
			uploadData := bytes.Repeat([]byte("resumable-upload!"), 64*1024)
			if _, err := client.UploadRemoteFileResuming(ctx, "files-host", shares[0].ID, "written.bin",
				bytes.NewReader(uploadData), int64(len(uploadData)), nil); err != nil {
				t.Fatalf("authenticated upload: %v", err)
			}
			onHost, err := os.ReadFile(filepath.Join(shareDir, "written.bin"))
			if err != nil || !bytes.Equal(onHost, uploadData) {
				t.Fatalf("remote upload invalid: %v", err)
			}
			// ACL revocation is enforced by the host even with a live Relay.
			updated, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			delete(updated.Shares[0].Permissions, "files-client")
			if _, err := store.Save(updated); err != nil {
				t.Fatal(err)
			}
			if _, err := client.RemoteFileShares(ctx, "files-host"); err != nil {
				// The share inventory is allowed to succeed empty; connection
				// errors here are not an authorization bypass.
			}
			if _, err := client.RemoteFileList(ctx, "files-host", shares[0].ID, "."); err == nil {
				t.Fatal("revoked folder ACL still allowed remote access")
			}
		})
	}
}
