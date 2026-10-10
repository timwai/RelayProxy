package routing

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// Host-wide rules and explicit exceptions are the only ABP constructs that
// the routing engine can implement without access to the full HTTPS URL.
func TestParseSubscriptionGFWList(t *testing.T) {
	lines := strings.Join([]string{
		"[Adblock Plus 2.0]",
		"! comment",
		"||google.com^",
		"||google.com^",
		"@@||accounts.google.com^",
		"||youtube.com",
		"|https://example.net/",
		"|https://example.net/private/path",
		"/blocked-by-regex/",
		"||widening.example^$third-party",
		"203.0.113.0/24",
		"192.0.2.15",
		"", ""}, "\n")
	for _, encoded := range []bool{false, true} {
		data := []byte(lines)
		if encoded {
			data = []byte(base64.StdEncoding.EncodeToString(data))
		}
		got, excluded, skipped, err := parseSubscription(data)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{".google.com", ".youtube.com", ".example.net", "203.0.113.0/24", "192.0.2.15"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("base64=%v targets=%#v", encoded, got)
		}
		if !reflect.DeepEqual(excluded, []string{".accounts.google.com"}) {
			t.Fatalf("exceptions=%#v", excluded)
		}
		if skipped != 3 {
			t.Fatalf("skipped=%d want 3", skipped)
		}
	}
}

func TestSubscriptionPrecedenceAndExceptions(t *testing.T) {
	sub := Subscription{Name: "GFWList", URL: "https://example.org/list.txt", Enabled: true, Action: ActionProxy, ExitID: "exit-s"}
	cfg := Config{Mode: ModeRule, DefaultAction: ActionDirect,
		Rules:         []Rule{{Name: "manual-reject", Enabled: true, Action: ActionReject, Targets: []string{"private.google.com"}}},
		Subscriptions: []Subscription{sub}}
	engine, err := newEngineSnapshot(cfg) // no network or worker in this test
	if err != nil {
		t.Fatal(err)
	}
	engine.subscriptions[0] = compiledSubscription{
		config:   sub,
		included: compileSubscriptionMatcher([]string{".google.com", "203.0.113.0/24"}),
		excluded: compileSubscriptionMatcher([]string{".accounts.google.com"}),
		status:   SubscriptionStatus{Rules: 2},
	}
	cases := []struct {
		host, ip string
		action   Action
		rule     string
	}{
		{"private.google.com", "", ActionReject, "manual-reject"},
		{"mail.google.com", "", ActionProxy, "订阅：GFWList"},
		{"accounts.google.com", "", ActionDirect, "default"},
		{"notgoogle.com", "", ActionDirect, "default"},
		{"", "203.0.113.42", ActionProxy, "订阅：GFWList"},
	}
	for _, c := range cases {
		got := engine.DecideFlow(Flow{Host: c.host, IP: c.ip, Port: 443, Protocol: "tcp"})
		if got.Action != c.action || got.Rule != c.rule {
			t.Errorf("%#v got %+v", c, got)
		}
		if got.Action == ActionProxy && got.ExitID != "exit-s" {
			t.Errorf("lost override %+v", got)
		}
	}
	cfg.Mode = ModeGlobalProxy
	if err := engine.Reload(Config{Mode: ModeGlobalProxy, Subscriptions: []Subscription{{Name: sub.Name, URL: sub.URL, Enabled: false, Action: ActionProxy}}}); err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	got := engine.Decide("accounts.google.com", 443)
	if got.Rule != "global_proxy" {
		t.Fatalf("global mode ignored: %+v", got)
	}
}

func TestSubscriptionValidationRejectsUnsafeURLs(t *testing.T) {
	cases := []string{
		"http://example.org/list", "https://localhost/list", "https://127.0.0.1/list",
		"https://[::1]/list", "https://localhost.local/list", "https://example.org:8443/list",
		"https://user:pass@example.org/list", "https://example.org/list#fragment",
		"https://example.org/list ",
	}
	for _, u := range cases {
		cfg := Config{Subscriptions: []Subscription{{Name: "unsafe", URL: u, Enabled: false, Action: ActionProxy}}}
		if err := ValidateConfig(cfg); err == nil {
			t.Errorf("accepted URL %q", u)
		}
	}
	if err := ValidateConfig(Config{Subscriptions: []Subscription{{Name: "valid", URL: "https://raw.githubusercontent.com/gfwlist/gfwlist/master/gfwlist.txt", Enabled: true, Action: ActionProxy}}}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(Config{Subscriptions: []Subscription{
		{Name: "a", URL: "https://example.org/list"},
		{Name: "b", URL: "https://example.org/list"},
	}}); err == nil {
		t.Fatal("accepted duplicate subscriptions")
	}
}

func TestSubscriptionFailedParsingDoesNotProduceCatchAll(t *testing.T) {
	for _, payload := range [][]byte{[]byte("! comment\n||example.com/path\n"), []byte(""), []byte("https://example.org/path")} {
		if targets, _, _, err := parseSubscription(payload); err == nil || len(targets) > 0 {
			t.Fatalf("expected failure for %q", payload)
		}
	}
}
