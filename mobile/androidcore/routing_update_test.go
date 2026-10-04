package androidcore

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/agent/routing"
)

func TestRoutingUpdateKeepsExistingTCPAndRejectsNewFlows(t *testing.T) {
	c, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","routing":{"mode":"direct"}}`, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		peer, err := l.Accept()
		if err == nil {
			defer peer.Close()
			io.Copy(peer, peer)
		}
	}()
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	conn, err := c.routingDialer.DialTCP(context.Background(), "", "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if err := c.SetRoutingConfig(`{"mode":"rule","default_action":"REJECT"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hello" {
		t.Fatal("existing flow interrupted", err)
	}
	if _, err := c.routingDialer.DialTCP(context.Background(), "", "127.0.0.1", port); err == nil {
		t.Fatal("new flow ignored reject")
	}
	if err := c.SetRoutingConfig(`{"mode":"invalid"}`); err == nil {
		t.Fatal("invalid update accepted")
	}
	if d := c.routingDialer.Engine().Decide("example.com", 443); d.Action != routing.ActionReject {
		t.Fatalf("invalid update lost policy: %+v", d)
	}
	var status statusSnapshot
	if err := json.Unmarshal([]byte(c.StatusJSON()), &status); err != nil || status.RoutingMode != routing.ModeRule {
		t.Fatal(status, err)
	}
}


func TestRoutingUpdatePreservesDisabledRulesOrderAndExitOverride(t *testing.T) {
	c, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","routing":{"mode":"global_proxy"}}`, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	err = c.SetRoutingConfig(`{
		"mode":"rule",
		"default_action":"PROXY",
		"rules":[
			{"name":"disabled-first","enabled":false,"targets":["api.example.com"],"action":"REJECT"},
			{"name":"specific-exit","enabled":true,"targets":["api.example.com"],"action":"PROXY","exit_id":"exit-b"},
			{"name":"fallback-direct","enabled":true,"targets":["*.example.com"],"action":"DIRECT"}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	decision := c.routingDialer.Engine().Decide("api.example.com", 443)
	if !decision.Matched || decision.Rule != "specific-exit" || decision.Action != routing.ActionProxy || decision.ExitID != "exit-b" {
		t.Fatalf("ordered rule did not override default exit: %+v", decision)
	}

	if err := c.SetRoutingConfig(`{"mode":"rule","default_action":"DIRECT","rules":[]}`); err != nil {
		t.Fatal(err)
	}
	decision = c.routingDialer.Engine().Decide("api.example.com", 443)
	if decision.Matched || decision.Rule != "default" || decision.Action != routing.ActionDirect {
		t.Fatalf("deleted rules remained active: %+v", decision)
	}
}

func TestVPNAuthenticationAndPackageGroups(t *testing.T) {
	c := &Client{cfg: clientConfig{VPNProxyToken: "private-secret"}}
	for _, test := range []struct {
		user, pass string
		ok         bool
	}{
		{"com.app.one|com.app.two", "private-secret", true},
		{"android", "private-secret", true},
		{androidUnknownProcess, "private-secret", true},
		{"com.app.one", "bad", false},
		{"com..app", "private-secret", false},
		{"com.1bad", "private-secret", false},
		{strings.Repeat("com.app|", 17) + "com.app", "private-secret", false},
	} {
		process, aliases, ok := c.authenticateVPNProxy(test.user, test.pass)
		if ok != test.ok {
			t.Fatalf("%s: %v", test.user, ok)
		}
		if test.user == "com.app.one|com.app.two" && (process != "com.app.one" || len(aliases) != 1 || aliases[0] != "com.app.two") {
			t.Fatal(process, aliases)
		}
	}
}

func TestPublicIdentityIDAllowsConfiguredTransport(t *testing.T) {
	for _, settings := range []string{`"tlsEnabled":false`, `"insecureTLS":true`} {
		_, err := normalizeConfig(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8",` + settings + `}`)
		if err != nil {
			t.Fatalf("public identity id rejected: %v", err)
		}
	}
}
