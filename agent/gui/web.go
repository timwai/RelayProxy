package gui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"relayproxy/agent/bridge"
	"relayproxy/agent/divert"
	"relayproxy/internal/webui"
)

const webAuthCookie = "relayproxy_agent_web"

// WebOptions configures the browser-accessible copy of the desktop UI.
type WebOptions struct {
	Listen string
	Port   int
	Token  string
}

// WebServer owns the browser management listener. Done is closed when the user
// requests shutdown from the page.
type WebServer struct {
	server   *http.Server
	listener net.Listener
	bridge   *bridge.UIBridge
	token    string
	loopback bool
	done     chan struct{}
	doneOnce sync.Once
}

func StartWeb(b *bridge.UIBridge, opts WebOptions) (*WebServer, error) {
	if b == nil {
		return nil, errors.New("web management requires an agent bridge")
	}
	host := strings.TrimSpace(opts.Listen)
	if host == "" {
		host = "127.0.0.1"
	}
	if opts.Port <= 0 || opts.Port > 65535 {
		return nil, fmt.Errorf("invalid web management port %d", opts.Port)
	}
	loopback := webLoopbackHost(host)
	token := strings.TrimSpace(opts.Token)
	if token != "" && len(token) < 32 {
		return nil, errors.New("web.token must be empty or at least 32 bytes")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(opts.Port)))
	if err != nil {
		return nil, fmt.Errorf("start web management listener: %w", err)
	}
	w := &WebServer{listener: listener, bridge: b, token: token, loopback: loopback, done: make(chan struct{})}
	mux := http.NewServeMux()
	w.registerRoutes(mux)
	w.server = &http.Server{
		Handler:           w.authorize(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := w.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[Web] 管理页面停止: %v", err)
			w.requestQuit()
		}
	}()
	return w, nil
}

func (w *WebServer) Addr() string          { return w.listener.Addr().String() }
func (w *WebServer) Done() <-chan struct{} { return w.done }

// BrowserURL returns a local URL suitable for opening the management page.
// Wildcard listener addresses are mapped to loopback; when authentication is
// enabled, the one-time query parameter is immediately exchanged for an
// HttpOnly cookie and removed by a redirect.
func (w *WebServer) BrowserURL() string {
	host, port, err := net.SplitHostPort(w.Addr())
	if err != nil {
		return ""
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil && addr.IsUnspecified() {
		if addr.Is6() {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	u := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/"}
	if w.token != "" {
		query := u.Query()
		query.Set("token", w.token)
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (w *WebServer) Close(ctx context.Context) error {
	if w == nil || w.server == nil {
		return nil
	}
	return w.server.Shutdown(ctx)
}

func (w *WebServer) requestQuit() { w.doneOnce.Do(func() { close(w.done) }) }

func (w *WebServer) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		setWebHeaders(rw)
		if w.loopback && !requestHostIsLoopback(r.Host) {
			http.Error(rw, "invalid Host for loopback management listener", http.StatusForbidden)
			return
		}
		if w.token != "" {
			if supplied := r.URL.Query().Get("token"); secureEqual(supplied, w.token) {
				http.SetCookie(rw, &http.Cookie{
					Name: webAuthCookie, Value: webCookieValue(w.token), Path: "/", HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
				})
				clean := *r.URL
				query := clean.Query()
				query.Del("token")
				clean.RawQuery = query.Encode()
				http.Redirect(rw, r, clean.RequestURI(), http.StatusSeeOther)
				return
			}
			if !w.authorized(r) {
				http.Error(rw, "unauthorized; open /?token=<web.token> once", http.StatusUnauthorized)
				return
			}
		}
		if isMutation(r.Method) && !sameOrigin(r) {
			http.Error(rw, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(rw, r)
	})
}

func (w *WebServer) authorized(r *http.Request) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") && secureEqual(strings.TrimSpace(auth[7:]), w.token) {
		return true
	}
	cookie, err := r.Cookie(webAuthCookie)
	return err == nil && secureEqual(cookie.Value, webCookieValue(w.token))
}

func webCookieValue(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func secureEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func sameOrigin(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && strings.EqualFold(u.Host, r.Host) &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func webLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && addr.IsLoopback()
}

func requestHostIsLoopback(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	return webLoopbackHost(host)
}

func setWebHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func (w *WebServer) registerRoutes(mux *http.ServeMux) {
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", webui.Handler()))
	mux.HandleFunc("GET /assets/", serveReactAsset)
	mux.HandleFunc("GET /", w.serveIndex)
	mux.HandleFunc("GET /connections", w.serveConnections)
	mux.HandleFunc("GET /web-bridge.js", w.serveWebBridge)
	for _, asset := range []string{"tailwind.js", "routing.js", "connections.js", "icon.png"} {
		name := asset
		mux.HandleFunc("GET /"+name, func(rw http.ResponseWriter, _ *http.Request) { serveEmbeddedAsset(rw, name) })
	}

	mux.HandleFunc("GET /api/status", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetStatus()) })
	mux.HandleFunc("GET /api/network-capabilities", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, divert.PlatformCapabilities()) })
	mux.HandleFunc("GET /api/diagnostics", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetDiagnostics()) })
	mux.HandleFunc("POST /api/speed-test", w.runSpeedTest)
	mux.HandleFunc("POST /api/proxy/custom-exits/test", w.testCustomExit)
	mux.HandleFunc("GET /api/proxy/exits", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetProxyExits()) })
	mux.HandleFunc("GET /api/rdp/targets", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetRDPTargets()) })
	mux.HandleFunc("POST /api/rdp/connect", w.connectRDP)
	mux.HandleFunc("POST /api/rdp/disconnect", func(rw http.ResponseWriter, _ *http.Request) {
		w.bridge.DisconnectRDP()
		writeWebJSON(rw, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/logs", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetLogs(500)) })
	mux.HandleFunc("GET /api/messages", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetMessages(500)) })
	mux.HandleFunc("DELETE /api/messages", func(rw http.ResponseWriter, _ *http.Request) {
		w.bridge.ClearMessages()
		writeWebJSON(rw, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /api/logs", func(rw http.ResponseWriter, _ *http.Request) {
		w.bridge.ClearLogs()
		writeWebJSON(rw, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/config", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = rw.Write([]byte(webConfigJSON(w.bridge)))
	})
	mux.HandleFunc("PUT /api/config", w.saveConfig)
	mux.HandleFunc("POST /api/reload", w.reloadConfig)
	mux.HandleFunc("POST /api/select-exit", w.selectExit)
	mux.HandleFunc("POST /api/autostart", w.setAutostart)
	mux.HandleFunc("GET /api/connections", func(rw http.ResponseWriter, _ *http.Request) { writeWebJSON(rw, w.bridge.GetConnections()) })
	mux.HandleFunc("DELETE /api/connections", func(rw http.ResponseWriter, _ *http.Request) {
		w.bridge.ClearConnections()
		writeWebJSON(rw, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/config-path", func(rw http.ResponseWriter, _ *http.Request) {
		writeWebJSON(rw, map[string]string{"path": w.bridge.ConfigPath()})
	})
	mux.HandleFunc("POST /api/quit", func(rw http.ResponseWriter, _ *http.Request) {
		writeWebJSON(rw, map[string]bool{"ok": true})
		w.requestQuit()
	})
}

func (w *WebServer) serveIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	// The same Vite bundle powers Wails, the browser console and the native
	// macOS WKWebView. Go-only developer builds still use the legacy page.
	if data, err := assets.ReadFile("react_dist/index.html"); err == nil {
		html := strings.Replace(string(data), "<head>", "<head><script src=\"/web-bridge.js\"></script>", 1)
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = rw.Write([]byte(html))
		return
	}
	serveHTMLAsset(rw, "assets/index.html")
}

// serveReactAsset serves only files under the embedded Vite assets directory;
// the authorization middleware protects bundles just like API endpoints.
func serveReactAsset(rw http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	if name == "" || !fs.ValidPath(name) || strings.Contains(name, "/") {
		http.NotFound(rw, r)
		return
	}
	data, err := assets.ReadFile("react_dist/assets/" + name)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		rw.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(rw, r)
		return
	}
	_, _ = rw.Write(data)
}

func (w *WebServer) serveConnections(rw http.ResponseWriter, r *http.Request) {
	// Existing bookmarks should open the React live monitor, rather than
	// falling back to the old standalone connections page. Keep the legacy
	// view available in Go-only developer builds without a Vite bundle.
	if _, err := assets.ReadFile("react_dist/index.html"); err == nil {
		http.Redirect(rw, r, "/?page=monitor", http.StatusSeeOther)
		return
	}
	serveHTMLAsset(rw, "assets/connections.html")
}

func serveHTMLAsset(rw http.ResponseWriter, name string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(rw, "embedded UI unavailable", http.StatusInternalServerError)
		return
	}
	shared := `<link rel="stylesheet" href="/ui/base.css"><script src="/ui/theme.js"></script><script src="/web-bridge.js"></script>`
	html := strings.Replace(string(data), "<head>", "<head>"+shared, 1)
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write([]byte(html))
}

func serveEmbeddedAsset(rw http.ResponseWriter, name string) {
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.Error(rw, "embedded asset not found", http.StatusNotFound)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".png"):
		rw.Header().Set("Content-Type", "image/png")
	}
	_, _ = rw.Write(data)
}

func (w *WebServer) serveWebBridge(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = rw.Write([]byte(webBridgeJS))
}

func (w *WebServer) saveConfig(rw http.ResponseWriter, r *http.Request) {
	var in bridge.ConfigUpdate
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	res, err := w.bridge.SaveConfig(in)
	writeWebMutation(rw, res, err)
}

func (w *WebServer) reloadConfig(rw http.ResponseWriter, _ *http.Request) {
	res, err := w.bridge.ReloadConfig()
	writeWebMutation(rw, res, err)
}

func (w *WebServer) selectExit(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		ExitID string `json:"exitId"`
	}
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	if err := w.bridge.SelectExit(in.ExitID); err != nil {
		writeWebError(rw, err)
		return
	}
	writeWebJSON(rw, map[string]bool{"ok": true})
}

func (w *WebServer) connectRDP(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		TargetID   string `json:"targetId"`
		AutoLaunch bool   `json:"autoLaunch"`
	}
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	target, err := w.bridge.ConnectRDP(in.TargetID, in.AutoLaunch)
	if err != nil {
		writeWebError(rw, err)
		return
	}
	status := w.bridge.GetStatus()
	writeWebJSON(rw, map[string]any{"ok": true, "target": target, "listenAddr": status.RDPListenAddr})
}

func (w *WebServer) testCustomExit(rw http.ResponseWriter, r *http.Request) {
	var in struct { ExitID string `json:"exitId"` }
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	result, err := w.bridge.TestCustomExit(in.ExitID)
	if err != nil {
		writeWebError(rw, err)
		return
	}
	writeWebJSON(rw, result)
}

func (w *WebServer) runSpeedTest(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		ExitID          string `json:"exitId"`
		DurationSeconds int    `json:"durationSeconds"`
	}
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	result, err := w.bridge.RunSpeedTest(in.ExitID, in.DurationSeconds)
	if err != nil {
		writeWebError(rw, err)
		return
	}
	writeWebJSON(rw, map[string]any{"ok": true, "result": result})
}

func (w *WebServer) setAutostart(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeWebJSON(rw, r, &in); err != nil {
		return
	}
	if err := w.bridge.SetAutoStart(in.Enabled); err != nil {
		writeWebError(rw, err)
		return
	}
	writeWebJSON(rw, map[string]bool{"ok": true})
}

func decodeWebJSON(rw http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(rw, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		http.Error(rw, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return err
	}
	return nil
}

func writeWebMutation(rw http.ResponseWriter, value any, err error) {
	if err != nil {
		writeWebError(rw, err)
		return
	}
	writeWebJSON(rw, value)
}

func writeWebError(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(rw).Encode(map[string]any{"ok": false, "message": err.Error()})
}

func writeWebJSON(rw http.ResponseWriter, value any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(rw).Encode(value)
}

// nonNilSlice keeps the desktop and Web JSON contracts consistent.\nfunc nonNilSlice[T any](items []T) []T {\n\tif items == nil {\n\t\treturn []T{}\n\t}\n\treturn items\n}\n\nfunc webConfigJSON(b *bridge.UIBridge) string {
	state, err := b.GetConfigState()
	if err != nil {
		data, _ := json.Marshal(map[string]any{"configError": err.Error(), "configPath": b.ConfigPath()})
		return string(data)
	}
	cfg := state.Config
	type proxyLeg struct {
		Enabled bool   `json:"enabled"`
		Listen  string `json:"listen"`
		Port    int    `json:"port"`
	}
	payload := struct {
		ConfigPath, ServerAddress, DeviceName, IdentityID, Transport, DefaultExitID string
		QUICPort, TCPPort, VerificationPopupTimeoutSec                              int
		TLSEnabled, ExitEnabled, AllowInternet, AllowPrivate, AllowLoopback         bool
		AccessMode, NetworkMode, Theme, Version                                     string
		AccessDomains, AccessCIDRs, RestartFields                                   []string
		SOCKS5, HTTP                                                                proxyLeg
		IsAutostart, MinimizeToTray, StartMinimized, RestartRequired, ReloadPending bool
		Routing, Network, Runtime, ExitUpstream, RDP, P2P, Direct                   any
		NetworkCapabilities                                                         divert.Capabilities
		CustomExits, UpstreamExitID                                                 any
		Revision                                                                    string
	}{
		ConfigPath: b.ConfigPath(), ServerAddress: cfg.Server.Address, QUICPort: cfg.Server.QUICPort,
		TCPPort: cfg.Server.TCPPort, TLSEnabled: cfg.IsServerTLSEnabled(),
		DeviceName: cfg.Device.Name, IdentityID: cfg.Device.IdentityID, Transport: cfg.Transport.Mode,
		SOCKS5:        proxyLeg{cfg.Proxy.SOCKS5.Enabled == nil || *cfg.Proxy.SOCKS5.Enabled, cfg.Proxy.SOCKS5.Listen, cfg.Proxy.SOCKS5.Port},
		HTTP:          proxyLeg{cfg.Proxy.HTTP.Enabled == nil || *cfg.Proxy.HTTP.Enabled, cfg.Proxy.HTTP.Listen, cfg.Proxy.HTTP.Port},
		DefaultExitID: cfg.Proxy.DefaultExitID, CustomExits: nonNilSlice(cfg.Proxy.CustomExits), UpstreamExitID: cfg.Exit.UpstreamExitID, ExitEnabled: cfg.Exit.Enabled == nil || *cfg.Exit.Enabled,
		AllowInternet: cfg.Exit.AllowInternet, AllowPrivate: cfg.Exit.AllowPrivateNetwork, AllowLoopback: cfg.Exit.AllowLoopback,
		AccessMode: cfg.Exit.Access.Mode, AccessDomains: cfg.Exit.Access.Domains, AccessCIDRs: cfg.Exit.Access.CIDRs,
		ExitUpstream: map[string]any{"mode": cfg.Exit.Upstream.Mode, "address": cfg.Exit.Upstream.Address, "username": cfg.Exit.Upstream.Username, "hasPassword": cfg.Exit.Upstream.Password != ""},
		RDP: map[string]any{
			"enabled": cfg.RDP.Enabled == nil || *cfg.RDP.Enabled,
			"address": cfg.RDP.Address,
		},
		P2P: map[string]any{
			"enabled": cfg.P2P.Enabled == nil || *cfg.P2P.Enabled, "mode": cfg.P2P.Mode,
			"punchTimeoutMs": cfg.P2P.PunchTimeoutMs, "keepaliveSec": cfg.P2P.KeepaliveSec,
			"idleTimeoutSec": cfg.P2P.IdleTimeoutSec, "maxExitSessions": cfg.P2P.MaxExitSessions,
			"fallback":    cfg.P2P.Fallback == nil || *cfg.P2P.Fallback,
			"upnpAllowed": cfg.P2P.UPnPAllowed != nil && *cfg.P2P.UPnPAllowed,
		},
		Direct:      map[string]any{"publicAdvertise": cfg.Direct.Public.Advertise},
		NetworkMode: cfg.Network.Mode, IsAutostart: b.IsAutoStart(), MinimizeToTray: cfg.IsMinimizeToTray(),
		StartMinimized: cfg.GUI.StartMinimized, Theme: cfg.GUI.Theme, Version: Version,
		VerificationPopupTimeoutSec: cfg.VerificationPopupTimeout(),
		Network:                     map[string]any{"mode": cfg.Network.Mode, "exclude_processes": cfg.Network.ExcludeProcesses},
		NetworkCapabilities:         divert.PlatformCapabilities(), Revision: state.Revision,
		RestartRequired: state.RestartRequired, RestartFields: state.RestartFields, ReloadPending: state.ReloadPending,
		Routing: map[string]any{"mode": cfg.Routing.Mode, "default_action": cfg.Routing.DefaultAction, "rules": cfg.Routing.Rules},
		Runtime: map[string]any{"serverAddress": state.Runtime.Server.Address, "quicPort": state.Runtime.Server.QUICPort,
			"tcpPort": state.Runtime.Server.TCPPort, "tlsEnabled": state.Runtime.IsServerTLSEnabled(), "insecureTls": state.Runtime.Server.InsecureTLS,
			"transport": state.Runtime.Transport.Mode, "networkMode": state.Runtime.Network.Mode,
			"rdp": map[string]any{
				"enabled": state.Runtime.RDP.Enabled == nil || *state.Runtime.RDP.Enabled,
				"address": state.Runtime.RDP.Address,
			},
			"direct": map[string]any{"publicAdvertise": state.Runtime.Direct.Public.Advertise},
			"p2p": map[string]any{
				"enabled": state.Runtime.P2P.Enabled == nil || *state.Runtime.P2P.Enabled, "mode": state.Runtime.P2P.Mode,
				"punchTimeoutMs": state.Runtime.P2P.PunchTimeoutMs, "keepaliveSec": state.Runtime.P2P.KeepaliveSec,
				"idleTimeoutSec": state.Runtime.P2P.IdleTimeoutSec, "maxExitSessions": state.Runtime.P2P.MaxExitSessions,
				"fallback":    state.Runtime.P2P.Fallback == nil || *state.Runtime.P2P.Fallback,
				"upnpAllowed": state.Runtime.P2P.UPnPAllowed != nil && *state.Runtime.P2P.UPnPAllowed,
			},
			"socks5": proxyLeg{state.Runtime.Proxy.SOCKS5.Enabled == nil || *state.Runtime.Proxy.SOCKS5.Enabled, state.Runtime.Proxy.SOCKS5.Listen, state.Runtime.Proxy.SOCKS5.Port},
			"http":   proxyLeg{state.Runtime.Proxy.HTTP.Enabled == nil || *state.Runtime.Proxy.HTTP.Enabled, state.Runtime.Proxy.HTTP.Listen, state.Runtime.Proxy.HTTP.Port}},
	}
	// Explicit tags are supplied by this map because the compact anonymous
	// structure above keeps the field assembly readable.
	data, _ := json.Marshal(map[string]any{
		"configPath": payload.ConfigPath, "serverAddress": payload.ServerAddress, "quicPort": payload.QUICPort,
		"tcpPort": payload.TCPPort, "tlsEnabled": payload.TLSEnabled,
		"deviceName": payload.DeviceName, "identityId": payload.IdentityID, "transport": payload.Transport,
		"socks5": payload.SOCKS5, "http": payload.HTTP, "defaultExitId": payload.DefaultExitID,
		"customExits": payload.CustomExits, "upstreamExitId": payload.UpstreamExitID,
		"exitEnabled": payload.ExitEnabled, "allowInternet": payload.AllowInternet,
		"allowPrivateNetwork": payload.AllowPrivate, "allowLoopback": payload.AllowLoopback,
		"accessMode": payload.AccessMode, "accessDomains": payload.AccessDomains, "accessCidrs": payload.AccessCIDRs,
		"exitUpstream": payload.ExitUpstream, "rdp": payload.RDP, "p2p": payload.P2P, "direct": payload.Direct, "publicDirectAdvertise": cfg.Direct.Public.Advertise, "insecureTls": cfg.Server.InsecureTLS,
		"networkMode": payload.NetworkMode, "isAutostart": payload.IsAutostart, "minimizeToTray": payload.MinimizeToTray,
		"startMinimized": payload.StartMinimized, "theme": payload.Theme, "version": payload.Version,
		"verificationPopupTimeoutSec": payload.VerificationPopupTimeoutSec,
		"routing":                     payload.Routing, "network": payload.Network, "networkCapabilities": payload.NetworkCapabilities,
		"runtime": payload.Runtime, "revision": payload.Revision, "restartRequired": payload.RestartRequired,
		"restartFields": payload.RestartFields, "reloadPending": payload.ReloadPending,
	})
	return string(data)
}

const webBridgeJS = `(function () {
  async function request(path, options) {
    options = options || {};
    options.credentials = 'same-origin';
    options.cache = 'no-store';
    var response = await fetch(path, options);
    var body = await response.text();
    if (!response.ok) {
      try { var parsed = JSON.parse(body); throw new Error(parsed.message || body); }
      catch (error) { if (error instanceof SyntaxError) throw new Error(body || ('HTTP ' + response.status)); throw error; }
    }
    return body;
  }
  function json(path, method, value) {
    return request(path, {method: method, headers: {'Content-Type':'application/json'}, body: JSON.stringify(value)});
  }
  window.goGetStatus = function () { return request('/api/status'); };
  window.goGetNetworkCapabilities = function () { return request('/api/network-capabilities'); };
  window.goGetDiagnostics = function () { return request('/api/diagnostics'); };
  window.goRunSpeedTest = function (exitId, durationSeconds) { return json('/api/speed-test', 'POST', {exitId:exitId, durationSeconds:durationSeconds}); };
  window.goTestCustomExit = function (exitId) { return json('/api/proxy/custom-exits/test', 'POST', {exitId:exitId}); };
  window.goGetProxyExits = function () { return request('/api/proxy/exits'); };
  window.goGetRDPTargets = function () { return request('/api/rdp/targets'); };
  window.goConnectRDP = async function (targetId, autoLaunch) {
    var nativeRDP = window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.relayproxyRDP;
    var body = await json('/api/rdp/connect', 'POST', {targetId:targetId, autoLaunch:!!autoLaunch && !nativeRDP});
    if (nativeRDP && autoLaunch) {
      var result = JSON.parse(body || '{}');
      if (!result.listenAddr) throw new Error('RDP 本地入口尚未建立');
      nativeRDP.postMessage({address:result.listenAddr});
    }
    return body;
  };
  window.goDisconnectRDP = function () { return json('/api/rdp/disconnect', 'POST', {}); };
  window.goGetLogs = function () { return request('/api/logs'); };
  window.goClearLogs = function () { return request('/api/logs', {method:'DELETE'}); };
  window.goGetMessages = function () { return request('/api/messages'); };
  window.goClearMessages = function () { return request('/api/messages', {method:'DELETE'}); };
  window.goGetConfig = function () { return request('/api/config'); };
  window.goSaveConfig = function (raw) { return request('/api/config', {method:'PUT', headers:{'Content-Type':'application/json'}, body:raw}); };
  window.goReloadConfig = function () { return json('/api/reload', 'POST', {}); };
  window.goSelectExit = async function (exitId) { await json('/api/select-exit', 'POST', {exitId:exitId}); return 'ok'; };
  window.goSetAutostart = function (enabled) { return json('/api/autostart', 'POST', {enabled:enabled}); };
  window.goOpenConnections = async function () { if (typeof window.relayNavigate === 'function') { window.relayNavigate('monitor'); return 'ok'; } window.open('/connections', '_blank', 'noopener'); return 'ok'; };
  window.goGetConnections = function () { return request('/api/connections'); };
  window.goClearConnections = function () { return request('/api/connections', {method:'DELETE'}); };
  window.goCopyClipboard = async function (text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(text);
      return 'ok';
    }
    var node = document.createElement('textarea');
    node.value = String(text || '');
    node.style.position = 'fixed';
    node.style.opacity = '0';
    document.body.appendChild(node);
    node.select();
    try {
      if (!document.execCommand('copy')) throw new Error('无法复制到剪贴板');
      return 'ok';
    } finally {
      node.remove();
    }
  };
  window.goOpenConfigDir = async function () { var out = JSON.parse(await request('/api/config-path')); alert('配置文件：' + out.path); };
  window.goRestart = function () {
    if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.relayproxyLifecycle) {
      window.webkit.messageHandlers.relayproxyLifecycle.postMessage('restart');
      return Promise.resolve('{"ok":true}');
    }
    return Promise.resolve('{"ok":false,"message":"浏览器模式请从系统服务管理器重启 relay-agent"}');
  };
  window.goQuit = function () {
    // A native macOS shell must own its process lifetime. Shut down its
    // bundled Agent from AppDelegate rather than racing HTTP server shutdown.
    if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.relayproxyLifecycle) {
      window.webkit.messageHandlers.relayproxyLifecycle.postMessage('quit');
      return Promise.resolve('{"ok":true}');
    }
    return json('/api/quit', 'POST', {});
  };
})();`
