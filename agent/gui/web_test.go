package gui

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
	"relayproxy/internal/config"
)

func newWebTestBridge(t *testing.T) *bridge.UIBridge {
	t.Helper()
	cfg := &config.AgentConfigFile{}
	cfg.Server.Address = "relay.example.test"
	cfg.Mode = "CLIENT"
	cfg.Device.Name = "web-test"
	if err := config.NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "relay-agent.yaml")
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	agent, err := app.NewAgent(app.AgentConfig{
		ServerAddress: cfg.Server.Address, QUICPort: cfg.Server.QUICPort, TCPPort: cfg.Server.TCPPort,
		Mode: cfg.Mode, TransportMode: cfg.Transport.Mode,
		SOCKS5Enabled: cfg.Proxy.SOCKS5.Enabled,
		SOCKS5Listen:  net.JoinHostPort(cfg.Proxy.SOCKS5.Listen, strconv.Itoa(cfg.Proxy.SOCKS5.Port)),
		HTTPEnabled:   cfg.Proxy.HTTP.Enabled,
		HTTPListen:    net.JoinHostPort(cfg.Proxy.HTTP.Listen, strconv.Itoa(cfg.Proxy.HTTP.Port)),
		ExitEnabled:   cfg.Exit.Enabled, DivertConfig: cfg.DivertConfig(), Routing: cfg.Routing,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	return bridge.NewUIBridge(agent, path)
}

func webTestHandler(b *bridge.UIBridge, loopback bool) (*WebServer, http.Handler) {
	w := &WebServer{bridge: b, loopback: loopback, done: make(chan struct{})}
	mux := http.NewServeMux()
	w.registerRoutes(mux)
	return w, w.authorize(mux)
}

func TestWebServesSameEmbeddedManagementPage(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `/web-bridge.js`) ||
		!strings.Contains(response.Body.String(), "RelayProxy") || !strings.Contains(response.Body.String(), "goRestart") {
		t.Fatalf("unexpected management page: %d %q", response.Code, response.Body.String())
	}
}

func TestWebManagementUsesUnifiedPersonalUI(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	for _, want := range []string{
		`/ui/base.css`,
		`/ui/theme.js`,
		`id="section-btn-network"`,
		`id="secondary-tabs"`,
		`id="tab-pane-connections"`,
		`id="inline-connections-body"`,
		`data-agent-theme="system"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("management page missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "web-token") {
		t.Fatal("Agent management page still exposes web token UI")
	}
	for _, forbidden := range []string{
		`src="icon.png"`,
		`id="brand-version"`,
		`<span>Local Agent</span>`,
		`<span>/</span>`,
		`RelayProxy 代理客户端`,
		`rp-reference-workspace`,
		`<small>Local agent</small>`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("management page still contains removed branding element %q", forbidden)
		}
	}
	if !strings.Contains(body, `<title>RelayProxy</title>`) ||
		!strings.Contains(body, `<div class="rp-reference-crumb"><b id="agent-section-label">概览</b></div>`) {
		t.Fatal("management page missing simplified title or section heading")
	}
	for _, forbidden := range []string{
		".rp-sidebar .rp-nav-label{display:none",
		".rp-sidebar .rp-nav-item span{display:none",
		".rp-sidebar>div:first-child>div:last-child{display:none",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("responsive Agent UI must preserve labeled sidebar; found %q", forbidden)
		}
	}
}

func TestWebManagementDoesNotExposeRDPTargetInventory(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	body := page.Body.String()
	for _, forbidden := range []string{"section-btn-rdp", "tab-pane-rdp", "rdp-targets", "goConnectRDP", "goGetRDPTargets"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Agent management page still exposes RDP inventory hook %q", forbidden)
		}
	}

	bridgeJS := httptest.NewRecorder()
	handler.ServeHTTP(bridgeJS, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/web-bridge.js", nil))
	for _, forbidden := range []string{"goGetRDPTargets", "goConnectRDP", "goDisconnectRDP", "/api/rdp/targets", "/api/rdp/connect", "/api/rdp/disconnect"} {
		if strings.Contains(bridgeJS.Body.String(), forbidden) {
			t.Fatalf("Agent web bridge still exposes RDP inventory hook %q", forbidden)
		}
	}

	for _, path := range []string{"/api/rdp/targets", "/api/rdp/connect", "/api/rdp/disconnect"} {
		method := http.MethodGet
		if path != "/api/rdp/targets" {
			method = http.MethodPost
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader("{}")))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("removed Agent RDP endpoint %s returned %d", path, rec.Code)
		}
	}
}

func TestWebManagementLoadsSharedFoundationBeforePageStyles(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	body := response.Body.String()
	shared := strings.Index(body, `/ui/base.css`)
	pageStyle := strings.Index(body, `<style>`)
	if shared < 0 || pageStyle < 0 || shared > pageStyle {
		t.Fatalf("shared design system must load before page styles: shared=%d style=%d", shared, pageStyle)
	}
	for _, want := range []string{
		`relayproxy-agent-navigation`,
		`rememberNavigation()`,
		`role', 'tab'`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Agent management page missing %q", want)
		}
	}
}

func TestWebRejectsCrossOriginMutation(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/reload", bytes.NewReader([]byte("{}")))
	request.Header.Set("Origin", "http://evil.test")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation status = %d", forbidden.Code)
	}
}

func TestWebConfigMutationAndQuit(t *testing.T) {
	b := newWebTestBridge(t)
	w, handler := webTestHandler(b, true)

	configResponse := httptest.NewRecorder()
	handler.ServeHTTP(configResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/config", nil))
	if bytes.Contains(configResponse.Body.Bytes(), []byte(":null")) {
		t.Fatalf("normalized GUI configuration contains null defaults: %s", configResponse.Body.String())
	}
	var state map[string]any
	if err := json.Unmarshal(configResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	revision, _ := state["revision"].(string)
	payload := `{"revision":"` + revision + `","proxy":{"defaultExitId":"exit-web"}}`
	request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1/api/config", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", (&url.URL{Scheme: "http", Host: request.Host}).String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("save response = %d %s", response.Code, response.Body.String())
	}
	if got := b.GetConfig().Proxy.DefaultExitID; got != "exit-web" {
		t.Fatalf("saved default exit = %q", got)
	}

	quit := httptest.NewRecorder()
	handler.ServeHTTP(quit, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/quit", bytes.NewReader([]byte("{}"))))
	if quit.Code != http.StatusOK {
		t.Fatalf("quit status = %d", quit.Code)
	}
	select {
	case <-w.Done():
	default:
		t.Fatal("web quit did not close Done")
	}
}

func TestWebRejectsNonLoopbackListener(t *testing.T) {
	if _, err := StartWeb(newWebTestBridge(t), WebOptions{Listen: "0.0.0.0", Port: 9090}); err == nil {
		t.Fatal("non-loopback Agent web listener was accepted")
	}
}
