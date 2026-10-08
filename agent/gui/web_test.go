package gui

import (
	"bytes"
	"context"
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

func webTestHandler(b *bridge.UIBridge, loopback bool, tokens ...string) (*WebServer, http.Handler) {
	token := ""
	if len(tokens) > 0 {
		token = tokens[0]
	}
	w := &WebServer{bridge: b, token: token, loopback: loopback, done: make(chan struct{})}
	mux := http.NewServeMux()
	w.registerRoutes(mux)
	return w, w.authorize(mux)
}

func hasBuiltReactWebUI() bool {
	_, err := assets.ReadFile("react_dist/index.html")
	return err == nil
}

func TestWebServesSameEmbeddedManagementPage(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `/web-bridge.js`) ||
		!strings.Contains(response.Body.String(), "RelayProxy") ||
		!(strings.Contains(response.Body.String(), "goRestart") || strings.Contains(response.Body.String(), "id=\"root\"")) {
		t.Fatalf("unexpected management page: %d %q", response.Code, response.Body.String())
	}
}

func TestWebManagementUsesUnifiedPersonalUI(t *testing.T) {
	if hasBuiltReactWebUI() {
		_, handler := webTestHandler(newWebTestBridge(t), true)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
		body := response.Body.String()
		for _, want := range []string{`id="root"`, `/web-bridge.js`, `/assets/`} {
			if response.Code != http.StatusOK || !strings.Contains(body, want) {
				t.Fatalf("React management page missing %q", want)
			}
		}
		if strings.Contains(strings.ToLower(body), "web-token") {
			t.Fatal("React management page exposes web token")
		}
		return
	}
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
		`id="secondary-tabs-wrap"`,
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
		`class="rp-reference-workspace"`,
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
	for _, want := range []string{
		`var showTabs = tabs.length > 1;`,
		`wrap.classList.toggle('hidden', !showTabs);`,
		`if (!showTabs) return;`,
		`if (tabs.length > 1) pane.setAttribute('aria-labelledby', 'tab-btn-' + id);`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("management page missing single-tab suppression logic %q", want)
		}
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

func TestWebStatusIncludesProxyExitInventory(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status response = %d %q", response.Code, response.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	exits, ok := status["proxyExits"].([]any)
	if !ok || exits == nil || len(exits) != 0 {
		t.Fatalf("status proxyExits = %#v, want empty array", status["proxyExits"])
	}
}

func TestWebManagementExposesServerAuthorizedProxyExitInventory(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	body := page.Body.String()
	if !hasBuiltReactWebUI() {
		for _, want := range []string{
			`id="cfg-exit-id"`, `<select id="cfg-exit-id"`,
			`id="speed-test-exit"`, `<select id="speed-test-exit"`,
			`id="routing-rule-exit"`, `<select id="routing-rule-exit"`,
			"goGetProxyExits", "fillProxyExitSelect", "已撤销、删除或当前不可用",
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("Agent management page missing proxy exit inventory hook %q", want)
			}
		}
	}

	bridgeJS := httptest.NewRecorder()
	handler.ServeHTTP(bridgeJS, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/web-bridge.js", nil))
	for _, want := range []string{"goGetProxyExits", "/api/proxy/exits"} {
		if !strings.Contains(bridgeJS.Body.String(), want) {
			t.Fatalf("Agent web bridge missing proxy exit inventory hook %q", want)
		}
	}

	exits := httptest.NewRecorder()
	handler.ServeHTTP(exits, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/proxy/exits", nil))
	if exits.Code != http.StatusOK || strings.TrimSpace(exits.Body.String()) != "[]" {
		t.Fatalf("empty proxy exit inventory response = %d %q", exits.Code, exits.Body.String())
	}
}

func TestWebManagementExposesServerAuthorizedRDPTargetActions(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	body := page.Body.String()
	if !hasBuiltReactWebUI() {
		for _, want := range []string{"section-btn-rdp", "tab-pane-rdp", "rdp-targets", "goConnectRDP", "goGetRDPTargets", "P2P TCP 直连"} {
			if !strings.Contains(body, want) {
				t.Fatalf("Agent management page missing RDP inventory hook %q", want)
			}
		}
	}

	bridgeJS := httptest.NewRecorder()
	handler.ServeHTTP(bridgeJS, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/web-bridge.js", nil))
	for _, want := range []string{"goGetRDPTargets", "goConnectRDP", "goDisconnectRDP", "/api/rdp/targets", "/api/rdp/connect", "/api/rdp/disconnect"} {
		if !strings.Contains(bridgeJS.Body.String(), want) {
			t.Fatalf("Agent web bridge missing RDP inventory hook %q", want)
		}
	}

	targets := httptest.NewRecorder()
	handler.ServeHTTP(targets, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/rdp/targets", nil))
	if targets.Code != http.StatusOK || strings.TrimSpace(targets.Body.String()) != "[]" {
		t.Fatalf("unapproved RDP inventory response = %d %q", targets.Code, targets.Body.String())
	}

	connect := httptest.NewRecorder()
	handler.ServeHTTP(connect, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/rdp/connect", strings.NewReader(`{"targetId":"not-authorized","autoLaunch":true}`)))
	if connect.Code != http.StatusBadRequest || !strings.Contains(connect.Body.String(), "not approved") {
		t.Fatalf("unauthorized RDP connect response = %d %q", connect.Code, connect.Body.String())
	}

	disconnect := httptest.NewRecorder()
	handler.ServeHTTP(disconnect, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/rdp/disconnect", strings.NewReader(`{}`)))
	if disconnect.Code != http.StatusOK {
		t.Fatalf("RDP disconnect response = %d %q", disconnect.Code, disconnect.Body.String())
	}
}

func TestWebManagementLoadsSharedFoundationBeforePageStyles(t *testing.T) {
	if hasBuiltReactWebUI() {
		t.Skip("React uses Vite CSS; legacy shared-style ordering is tested only for fallback builds")
	}
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
	for _, origin := range []string{
		"http://evil.test",
		"custom://127.0.0.1",
		"http://user@127.0.0.1",
		"http://127.0.0.1/path",
	} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/reload", bytes.NewReader([]byte("{}")))
		request.Header.Set("Origin", origin)
		forbidden := httptest.NewRecorder()
		handler.ServeHTTP(forbidden, request)
		if forbidden.Code != http.StatusForbidden {
			t.Errorf("cross-origin mutation %q status = %d", origin, forbidden.Code)
		}
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

func TestWebAllowsNonLoopbackListenerWithoutToken(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := StartWeb(newWebTestBridge(t), WebOptions{
		Listen: "0.0.0.0", Port: port, Token: strings.Repeat("t", 31),
	}); err == nil {
		t.Fatal("non-loopback Agent web listener accepted a short non-empty token")
	}

	server, err := StartWeb(newWebTestBridge(t), WebOptions{Listen: "0.0.0.0", Port: port})
	if err != nil {
		t.Fatalf("non-loopback Agent web listener without token was rejected: %v", err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if server.loopback {
		t.Fatal("0.0.0.0 listener was incorrectly marked as loopback")
	}
	if browserURL := server.BrowserURL(); (!strings.HasPrefix(browserURL, "http://127.0.0.1:") && !strings.HasPrefix(browserURL, "http://[::1]:")) ||
		strings.Contains(browserURL, "token=") {
		t.Fatalf("unexpected unauthenticated browser URL: %q", browserURL)
	}
}

func TestWebOptionalTokenProtectsRemoteManagement(t *testing.T) {
	_, openHandler := webTestHandler(newWebTestBridge(t), false)
	openRequest := httptest.NewRequest(http.MethodGet, "http://agent.test/api/status", nil)
	openResponse := httptest.NewRecorder()
	openHandler.ServeHTTP(openResponse, openRequest)
	if openResponse.Code != http.StatusOK {
		t.Fatalf("token-free remote request status = %d", openResponse.Code)
	}

	token := strings.Repeat("t", 32)
	_, handler := webTestHandler(newWebTestBridge(t), false, token)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://agent.test/api/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated remote request status = %d", unauthorized.Code)
	}

	loginRequest := httptest.NewRequest(http.MethodGet, "http://agent.test/?token="+token, nil)
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/" {
		t.Fatalf("token exchange response = %d location=%q", login.Code, login.Header().Get("Location"))
	}
	response := login.Result()
	if len(response.Cookies()) != 1 || response.Cookies()[0].Name != webAuthCookie || !response.Cookies()[0].HttpOnly ||
		response.Cookies()[0].SameSite != http.SameSiteStrictMode || response.Cookies()[0].Value == token {
		t.Fatalf("token exchange did not issue the protected cookie: %#v", response.Cookies())
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "http://agent.test/api/config", nil)
	authorizedRequest.AddCookie(response.Cookies()[0])
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated remote request status = %d", authorized.Code)
	}

	bearerRequest := httptest.NewRequest(http.MethodGet, "http://agent.test/api/status", nil)
	bearerRequest.Header.Set("Authorization", "Bearer "+token)
	bearer := httptest.NewRecorder()
	handler.ServeHTTP(bearer, bearerRequest)
	if bearer.Code != http.StatusOK {
		t.Fatalf("bearer-authenticated remote request status = %d", bearer.Code)
	}
}

func TestConnectionsPageSupportsStatusFilterClearAndNewestFirst(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)

	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connections", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("connections page status = %d", page.Code)
	}
	for _, want := range []string{
		`id="state-filter"`,
		`value="connecting"`,
		`value="active"`,
		`value="closed"`,
		`value="failed"`,
		`value="rejected"`,
		`id="clear"`,
		`data-sort="started_at"`,
		`.state.connecting`,
		`.state.active`,
		`.state.closed`,
		`.state.failed`,
		`.state.rejected`,
	} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatalf("connections page missing %q", want)
		}
	}

	script := httptest.NewRecorder()
	handler.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/connections.js", nil))
	for _, want := range []string{
		"sort = 'started_at'",
		"state !== 'all' && row.state !== state",
		"window.goClearConnections",
		"startedAt(record.started_at)",
	} {
		if !strings.Contains(script.Body.String(), want) {
			t.Fatalf("connections script missing %q", want)
		}
	}

	clear := httptest.NewRecorder()
	handler.ServeHTTP(clear, httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/api/connections", nil))
	if clear.Code != http.StatusOK || !strings.Contains(clear.Body.String(), `"ok":true`) {
		t.Fatalf("clear connections response = %d %s", clear.Code, clear.Body.String())
	}

	bridgeJS := httptest.NewRecorder()
	handler.ServeHTTP(bridgeJS, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/web-bridge.js", nil))
	if !strings.Contains(bridgeJS.Body.String(), "goClearConnections") {
		t.Fatal("browser bridge missing clear connections method")
	}
}

func TestMainWebConnectionsPaneMatchesRealtimeMonitorFeatures(t *testing.T) {
	if hasBuiltReactWebUI() {
		// React renders the monitor after hydration; verify the embedded
		// frontend instead of searching for legacy DOM IDs in Vite HTML.
		assertReactBundleContains(t, "实时监控", "连接列表", "清理历史", "全部状态", "started_at")
		return
	}
	_, handler := webTestHandler(newWebTestBridge(t), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("main web status = %d", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		`id="inline-conn-state"`,
		`onclick="clearConnectionsInline()"`,
		"Date.parse(a.started_at",
		"inline-conn-state ",
		"已清空已结束连接历史，活跃连接已保留",
		"连接时间 ↓",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("main web connections pane missing %q", want)
		}
	}
}

func TestAgentSpeedTestShowsPathAndQUICDiagnostics(t *testing.T) {
	if hasBuiltReactWebUI() {
		assertReactBundleContains(t, "出口双向测速", "sent_packet_loss_pct", "rtt_deviation_ms", "GSO", "goRunSpeedTest")
		return
	}
	_, handler := webTestHandler(newWebTestBridge(t), true)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("management page status = %d", response.Code)
	}
	body := response.Body.String()
	for _, want := range []string{
		"speedTestPathLabel",
		"speedTestQUICDetail",
		"public_direct_quic",
		"Public Direct QUIC",
		"sent_packet_loss_pct",
		"rtt_deviation_ms",
		"udp_read_buffer_bytes",
		"udp_write_buffer_bytes",
		"GSO ",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("Agent speed test diagnostics missing %q", want)
		}
	}
}
