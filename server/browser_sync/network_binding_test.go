package browsersync

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"relayproxy/server/session"
)

func TestNetworkBindingConsentAndRevoke(t *testing.T) {
	store := testBrowserStore(t)
	source, target, ruleID := approvedDeliveryRule(t, store)
	ctx := context.Background()
	if err := store.OfferNetworkBinding(ctx, source.ID, ruleID, "login.example.com", "dev_agent1234"); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptNetworkBinding(ctx, source.ID, ruleID, "login.example.com"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("source self-approved: %v", err)
	}
	if _, err := store.NetworkDestination(ctx, target.ID, "login.example.com"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("network used before target consent: %v", err)
	}
	if err := store.AcceptNetworkBinding(ctx, target.ID, ruleID, "elsewhere.example.com"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("different host accepted: %v", err)
	}
	if err := store.AcceptNetworkBinding(ctx, target.ID, ruleID, "login.example.com"); err != nil {
		t.Fatal(err)
	}
	id, err := store.NetworkDestination(ctx, target.ID, "login.example.com")
	if err != nil || id != "dev_agent1234" {
		t.Fatalf("wrong active exit %s: %v", id, err)
	}
	if err := store.RevokeNetworkBinding(ctx, target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NetworkDestination(ctx, target.ID, "login.example.com"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("network access survived revocation: %v", err)
	}
}
func TestNetworkBindingHostRestrictions(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "LOCALHOST", "localhost", "a.local", "a.internal", "a.arpa", "[::1]", "bad_.example.com", "bad.example.com:443", "a..example.com", "EXAMPLE.COM", "a.example.com/evil", "example.com\nProxy-Authorization"} {
		if validNetworkHost(host) {
			t.Fatalf("invalid host allowed: %q", host)
		}
	}
	if !validNetworkHost("login.example.com") {
		t.Fatal("safe exact host rejected")
	}
}
func TestNetworkProxyLeaseAuthAndCONNECTIsolation(t *testing.T) {
	store := testBrowserStore(t)
	source, target, ruleID := approvedDeliveryRule(t, store)
	ctx := context.Background()
	if err := store.OfferNetworkBinding(ctx, source.ID, ruleID, "login.example.com", "dev_agent1234"); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptNetworkBinding(ctx, target.ID, ruleID, "login.example.com"); err != nil {
		t.Fatal(err)
	}
	proxy := NewNetworkProxy(store, session.NewManager(), "relay.example.com:21090", []string{"dev_agent1234"})
	lease, err := proxy.Issue(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := lease["token"].(string)
	if len(token) < 40 {
		t.Fatal("weak token")
	}
	// Never forward requests without authenticated HTTPS CONNECT credentials.
	for _, tc := range []struct {
		method, host string
		tls          bool
		want         int
	}{
		{"GET", "login.example.com:443", true, http.StatusMethodNotAllowed},
		{"CONNECT", "login.example.com:443", false, http.StatusMethodNotAllowed},
		{"CONNECT", "login.example.com:443", true, http.StatusProxyAuthRequired},
	} {
		r := httptest.NewRequest(tc.method, "https://relay.example.com/", nil)
		r.Host = tc.host
		if !tc.tls {
			r.TLS = nil
		}
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %v: got %d want %d", tc.method, tc.tls, w.Code, tc.want)
		}
	}
	if _, err := proxy.checkToken(""); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("anonymous request accepted: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("browser:" + token))
	if got, err := proxy.checkToken("Basic " + encoded); err != nil || got.deviceID != target.ID {
		t.Fatalf("valid browser token rejected: %v", err)
	}
	now := time.Now()
	proxy.mu.Lock()
	for hash, value := range proxy.leases {
		value.expires = now.Add(-time.Second)
		proxy.leases[hash] = value
	}
	proxy.mu.Unlock()
	if _, err := proxy.checkToken("Basic " + encoded); err == nil {
		t.Fatal("expired token accepted")
	}
}
