package androidcore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	proxyp2p "relayproxy/agent/p2p"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestPowerConstrainedStatusBeforeStart(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	client.SetPowerConstrained(true)
	var status statusSnapshot
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if !status.PowerConstrained {
		t.Fatalf("powerConstrained=%v, want true", status.PowerConstrained)
	}
	if status.ConnectionState == "" {
		t.Fatal("connection state is missing")
	}

	client.SetPowerConstrained(false)
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if status.PowerConstrained {
		t.Fatal("powerConstrained remained enabled")
	}
}

func TestSetPowerConstrainedAfterStopIsIgnored(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	client.SetPowerConstrained(true)

	var status statusSnapshot
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if status.PowerConstrained {
		t.Fatal("closed client accepted a power-profile mutation")
	}
}

func TestIdentityIDRequiresGeneratedLetterDigitFormat(t *testing.T) {
	if !validIdentityID("a1b2c3d4e5f6g7h8") {
		t.Fatal("valid generated identity id was rejected")
	}
	for _, value := range []string{
		"team-a", "abcdefghijklmnop", "1234567890123456", "A1b2c3d4e5f6g7h8", "a1b2c3d4e5f6g7-8",
	} {
		if validIdentityID(value) {
			t.Fatalf("invalid identity id %q was accepted", value)
		}
	}
}

func TestControlOnlyConfigRequestsAndroidCapabilities(t *testing.T) {
	client, err := NewClient(`{
		"serverAddress":"relay.example.com",
		"identityId":"a1b2c3d4e5f6g7h8",
		"exitEnabled":false,
		"clientEnabled":false,
		"requestedCapabilities":["proxy.client","proxy.exit"]
	}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	want := []string{protocol.CapabilityProxyClient, protocol.CapabilityProxyExit}
	if got := client.requestedCapabilities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requested capabilities = %v, want %v", got, want)
	}
	if client.cfg.ClientEnabled || *client.cfg.ExitEnabled {
		t.Fatal("control-only config enabled a data-plane capability")
	}
}

func TestStatusJSONExposesP2PFailureReason(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	manager := proxyp2p.NewManager(context.Background(), nil, nil, time.Minute)
	t.Cleanup(func() { _ = manager.Close() })
	client.mu.Lock()
	client.proxyP2P = manager
	client.status.SelectedExit = "exit-device"
	client.mu.Unlock()

	manager.EnsureClient("exit-device")
	deadline := time.Now().Add(time.Second)
	for {
		var status statusSnapshot
		if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
			t.Fatal(err)
		}
		if status.P2PError != "" {
			if status.P2PState != "COOLDOWN" {
				t.Fatalf("p2pState=%q, want COOLDOWN", status.P2PState)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("P2P failure reason was not exposed in status JSON")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAndroidProxyExitInventoryRevisionOrdering(t *testing.T) {
	client, err := NewClient(`{
		"serverAddress":"relay.example.com",
		"identityId":"a1b2c3d4e5f6g7h8",
		"exitEnabled":false,
		"clientEnabled":true,
		"socks5Enabled":true,
		"requestedCapabilities":["proxy.client"]
	}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	client.mu.Lock()
	client.proxyExitRevision = 5
	client.status.ProxyExits = []protocol.ProxyExit{{DeviceID: "exit-new", Name: "New", Online: true}}
	client.mu.Unlock()

	client.refreshProxyExits(nil, []protocol.ProxyExit{{DeviceID: "exit-old", Name: "Old", Online: true}}, 4)
	client.mu.RLock()
	if len(client.status.ProxyExits) != 1 || client.status.ProxyExits[0].DeviceID != "exit-new" || client.proxyExitRevision != 5 {
		got, revision := cloneProxyExits(client.status.ProxyExits), client.proxyExitRevision
		client.mu.RUnlock()
		t.Fatalf("older Android inventory replaced current snapshot: exits=%+v revision=%d", got, revision)
	}
	client.mu.RUnlock()

	client.refreshProxyExits(nil, []protocol.ProxyExit{{DeviceID: "exit-replay", Name: "Replay", Online: true}}, 5)
	client.mu.RLock()
	if len(client.status.ProxyExits) != 1 || client.status.ProxyExits[0].DeviceID != "exit-replay" || client.proxyExitRevision != 5 {
		got, revision := cloneProxyExits(client.status.ProxyExits), client.proxyExitRevision
		client.mu.RUnlock()
		t.Fatalf("same-revision Android recovery snapshot was ignored: exits=%+v revision=%d", got, revision)
	}
	client.mu.RUnlock()

	client.refreshProxyExits(nil, []protocol.ProxyExit{{DeviceID: "exit-latest", Name: "Latest", Online: true}}, 6)
	client.mu.RLock()
	defer client.mu.RUnlock()
	if len(client.status.ProxyExits) != 1 || client.status.ProxyExits[0].DeviceID != "exit-latest" || client.proxyExitRevision != 6 {
		t.Fatalf("newer Android inventory was not applied: exits=%+v revision=%d", client.status.ProxyExits, client.proxyExitRevision)
	}
}

func TestAndroidLegacyProxyExitRefreshKeepsRevision(t *testing.T) {
	client, err := NewClient(`{
		"serverAddress":"relay.example.com",
		"identityId":"a1b2c3d4e5f6g7h8",
		"exitEnabled":false,
		"clientEnabled":true,
		"socks5Enabled":true,
		"requestedCapabilities":["proxy.client"]
	}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	client.mu.Lock()
	client.proxyExitRevision = 9
	client.mu.Unlock()
	client.refreshProxyExits(nil, []protocol.ProxyExit{{DeviceID: "exit-b", Online: true}}, 0)

	client.mu.RLock()
	defer client.mu.RUnlock()
	if len(client.status.ProxyExits) != 1 || client.status.ProxyExits[0].DeviceID != "exit-b" {
		t.Fatalf("legacy Android inventory refresh was not applied: %+v", client.status.ProxyExits)
	}
	if client.proxyExitRevision != 9 {
		t.Fatalf("legacy Android refresh reset revision: %d", client.proxyExitRevision)
	}
}

func TestAndroidStatusRedactsPublicDirectTicket(t *testing.T) {
	source := []protocol.ProxyExit{{
		DeviceID: "exit-direct", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available: true, Transport: "quic",
			Ticket: []byte("android-secret-ticket"), TicketExpiresAt: 456,
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol: protocol.PublicDirectEndpointProtocolUDP,
				Address:  "203.0.113.20:35820", Source: protocol.PublicDirectEndpointManual,
				Verified: true, CertFingerprint: "sha256:test",
			}},
		}},
	}}
	redacted := redactProxyExitTickets(source)
	if len(redacted) != 1 || redacted[0].Direct == nil || redacted[0].Direct.Public == nil {
		t.Fatalf("redacted exits=%+v", redacted)
	}
	if len(redacted[0].Direct.Public.Ticket) != 0 {
		t.Fatal("Android UI inventory exposed the Public Direct ticket")
	}
	if redacted[0].Direct.Public.TicketExpiresAt != 456 ||
		redacted[0].Direct.Public.Endpoints[0].CertFingerprint != "sha256:test" {
		t.Fatalf("redaction removed non-secret metadata: %+v", redacted[0].Direct.Public)
	}
	if string(source[0].Direct.Public.Ticket) != "android-secret-ticket" {
		t.Fatal("Android redaction mutated the internal ticket")
	}
}

func TestAndroidProxyPathModeNormalization(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "default",
			raw:  `{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`,
			want: "auto",
		},
		{
			name: "direct only",
			raw:  `{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","proxyPathMode":"direct_only"}`,
			want: "direct_only",
		},
		{
			name: "p2p only",
			raw:  `{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","proxyPathMode":"p2p_only","proxyP2pEnabled":true}`,
			want: "p2p_only",
		},
		{
			name: "relay only",
			raw:  `{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","proxyPathMode":"relay_only"}`,
			want: "relay_only",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := normalizeConfig(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ProxyPathMode != tc.want {
				t.Fatalf("proxyPathMode=%q, want %q", cfg.ProxyPathMode, tc.want)
			}
		})
	}

	if _, err := normalizeConfig(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","proxyPathMode":"invalid"}`); err == nil {
		t.Fatal("invalid proxyPathMode was accepted")
	}
	if _, err := normalizeConfig(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","proxyPathMode":"p2p_only","proxyP2pEnabled":false}`); err == nil {
		t.Fatal("p2p_only without P2P enabled was accepted")
	}
}

func TestAndroidMessageQueueDrainsOnce(t *testing.T) {
	client := &Client{}
	client.enqueueMessage(protocol.PushMessage{
		ID: "msg-1", Content: "验证码 482931", VerificationCode: "482931",
		VerificationRule: "默认验证码", Popup: true, PopupType: "verification_code",
	})
	var messages []protocol.PushMessage
	if err := json.Unmarshal([]byte(client.PopMessagesJSON()), &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "msg-1" || !messages[0].Popup || messages[0].PopupType != "verification_code" {
		t.Fatalf("unexpected messages: %+v", messages)
	}
	if got := client.PopMessagesJSON(); got != "[]" {
		t.Fatalf("message queue was not drained: %s", got)
	}
}

func TestAndroidBrutalBandwidthNormalization(t *testing.T) {
	cfg, err := normalizeConfig(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","brutalUpMbps":120,"brutalDownMbps":450,"disableLossCompensation":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrutalUpMbps != 120 || cfg.BrutalDownMbps != 450 || !cfg.DisableLossCompensation {
		t.Fatalf("Brutal config=%+v", cfg)
	}
	for _, raw := range []string{
		`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","brutalUpMbps":-1}`,
		`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","brutalDownMbps":1000001}`,
	} {
		if _, err := normalizeConfig(raw); err == nil {
			t.Fatalf("invalid Brutal config accepted: %s", raw)
		}
	}
}


func TestAndroidStatusSerializesP2PQUICDiagnostics(t *testing.T) {
	status := statusSnapshot{
		P2PQUIC: &tunnel.QUICDiagnostics{
			CongestionController: "brutal",
			CongestionTargetBPS:  25_000_000,
		},
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	p2p, ok := decoded["p2pQuic"].(map[string]any)
	if !ok {
		t.Fatalf("p2pQuic missing from status JSON: %s", raw)
	}
	if got := p2p["congestion_controller"]; got != "brutal" {
		t.Fatalf("p2pQuic congestion_controller=%v", got)
	}
	if got := p2p["congestion_target_bps"]; got != float64(25_000_000) {
		t.Fatalf("p2pQuic congestion_target_bps=%v", got)
	}
}
