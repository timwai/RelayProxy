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

func webTestHandler(b *bridge.UIBridge, loopback bool, token string) (*WebServer, http.Handler) {
	w := &WebServer{bridge: b, loopback: loopback, token: token, done: make(chan struct{})}
	mux := http.NewServeMux()
	w.registerRoutes(mux)
	return w, w.authorize(mux)
}

func TestWebServesSameEmbeddedManagementPage(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true, "")
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `/web-bridge.js`) ||
		!strings.Contains(response.Body.String(), "RelayProxy") || !strings.Contains(response.Body.String(), "goRestart") {
		t.Fatalf("unexpected management page: %d %q", response.Code, response.Body.String())
	}
}

func TestWebTokenBootstrapAndOriginProtection(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), false, "secret-token")
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "http://agent.test/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	bootstrap := httptest.NewRecorder()
	handler.ServeHTTP(bootstrap, httptest.NewRequest(http.MethodGet, "http://agent.test/?token=secret-token", nil))
	if bootstrap.Code != http.StatusSeeOther || len(bootstrap.Result().Cookies()) != 1 || !bootstrap.Result().Cookies()[0].HttpOnly {
		t.Fatalf("token bootstrap did not issue a secure session cookie: %+v", bootstrap.Result())
	}

	request := httptest.NewRequest(http.MethodPost, "http://agent.test/api/reload", bytes.NewReader([]byte("{}")))
	request.AddCookie(bootstrap.Result().Cookies()[0])
	request.Header.Set("Origin", "http://evil.test")
	forbidden := httptest.NewRecorder()
	handler.ServeHTTP(forbidden, request)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation status = %d", forbidden.Code)
	}
}

func TestWebConfigMutationAndQuit(t *testing.T) {
	b := newWebTestBridge(t)
	w, handler := webTestHandler(b, true, "")

	configResponse := httptest.NewRecorder()
	handler.ServeHTTP(configResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/config", nil))
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

func TestWebRejectsNonLoopbackWithoutToken(t *testing.T) {
	if _, err := StartWeb(newWebTestBridge(t), WebOptions{Listen: "0.0.0.0", Port: 9090}); err == nil {
		t.Fatal("non-loopback listener without token was accepted")
	}
	if _, err := StartWeb(newWebTestBridge(t), WebOptions{Listen: "0.0.0.0", Port: 9090, Token: "too-short"}); err == nil {
		t.Fatal("non-loopback listener with a weak token was accepted")
	}
}
