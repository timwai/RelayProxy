package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

// The handler only queries addresses/transport; no streams are opened.
type diagnosticAddressSession struct {
	tunnel.TunnelSession
	remote string
}

func (*diagnosticAddressSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (*diagnosticAddressSession) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 20000}
}
func (s *diagnosticAddressSession) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(s.remote), Port: 30000}
}
func (*diagnosticAddressSession) Close() error { return nil }

func TestActiveSessionDiagnosticsAreAuthenticatedScopedAndIdentifyReconnects(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	owner, cookie := apiUser(t, router, "diagnostic-owner")
	other, _ := apiUser(t, router, "diagnostic-other")
	connected := time.Now().UTC().Add(-time.Minute)
	register := func(id, ownerID, remote string, at time.Time) *session.DeviceSession {
		t.Helper()
		if err := router.db.UpsertDevice(&repository.Device{ID: id, Name: id, OwnerUserID: ownerID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		d := &session.DeviceSession{
			DeviceID: id, DeviceName: id, OwnerUserID: ownerID, Mode: "CLIENT",
			Transport: tunnel.TransportTLS, ConnectedAt: at,
			Tunnel: &diagnosticAddressSession{remote: remote},
		}
		d.ActiveStreams.Store(1)
		router.sessions.Register(d)
		return d
	}
	ownSession := register("own-diagnostic-client", owner.ID, "192.0.2.2", connected)
	register("other-diagnostic-client", other.ID, "192.0.2.99", connected)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/active", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated diagnostics: %d", unauthorized.Code)
	}
	read := func(wantConnected time.Time, wantPeer string) {
		t.Helper()
		before := time.Now().UTC()
		response := apiRequest(router, cookie, http.MethodGet, "/api/v1/sessions/active")
		var rows []struct {
			ClientDeviceID  string                     `json:"clientDeviceId"`
			ConnectedAt     time.Time                  `json:"connectedAt"`
			SampledAt       time.Time                  `json:"sampledAt"`
			Diagnostics     *tunnel.SessionDiagnostics `json:"tunnelDiagnostics"`
			PeerDiagnostics *session.DeviceDiagnostics `json:"peerDiagnostics"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != 1 {
			t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
		}
		row := rows[0]
		if row.ClientDeviceID != "own-diagnostic-client" || strings.Contains(response.Body.String(), "192.0.2.99") {
			t.Fatalf("another owner's diagnostics leaked: %s", response.Body.String())
		}
		if !row.ConnectedAt.Equal(wantConnected) || row.SampledAt.Before(before) || row.SampledAt.After(time.Now()) {
			t.Fatalf("wrong session generation or sample time: %+v", row)
		}
		if d := row.Diagnostics; d == nil || d.Local != "192.0.2.1:20000" || d.Remote != "192.0.2.2:30000" || d.Transport != tunnel.TransportTLS || d.QUIC != nil {
			t.Fatalf("wrong endpoint or fabricated QUIC counters: %+v", d)
		}
		if wantPeer != "" {
			if row.PeerDiagnostics == nil || string(row.PeerDiagnostics.Payload) != wantPeer {
				t.Fatalf("missing peer diagnostics: %+v", row.PeerDiagnostics)
			}
		} else if row.PeerDiagnostics != nil {
			t.Fatalf("fabricated peer diagnostics: %+v", row.PeerDiagnostics)
		}
	}
	read(connected, "")
	ownSession.ActiveStreams.Store(0)
	activePeer := `{"connections":[{"state":"active"}]}`
	ownSession.SetDiagnostics(json.RawMessage(activePeer))
	read(connected, activePeer)
	reconnected := connected.Add(30 * time.Second)
	register("own-diagnostic-client", owner.ID, "192.0.2.2", reconnected)
	read(reconnected, "")
}
