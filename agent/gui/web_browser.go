//go:build !nativecore

package gui

import (
	"net/http"
	"strings"

	"relayproxy/internal/webui"
)

func (w *WebServer) registerBrowserRoutes(mux *http.ServeMux) {
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", webui.Handler()))
	mux.HandleFunc("GET /", w.serveIndex)
	mux.HandleFunc("GET /connections", w.serveConnections)
	mux.HandleFunc("GET /web-bridge.js", w.serveWebBridge)
	for _, asset := range []string{"tailwind.js", "routing.js", "connections.js", "icon.png"} {
		name := asset
		mux.HandleFunc("GET /"+name, func(rw http.ResponseWriter, _ *http.Request) { serveEmbeddedAsset(rw, name) })
	}
}

func (w *WebServer) serveIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	serveHTMLAsset(rw, "assets/index.html")
}

func (w *WebServer) serveConnections(rw http.ResponseWriter, _ *http.Request) {
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
  window.goGetDiagnostics = function () { return request('/api/diagnostics'); };
  window.goRunSpeedTest = function (exitId, durationSeconds) { return json('/api/speed-test', 'POST', {exitId:exitId, durationSeconds:durationSeconds}); };
  window.goGetProxyExits = function () { return request('/api/proxy/exits'); };
  window.goGetRDPTargets = function () { return request('/api/rdp/targets'); };
  window.goConnectRDP = function (targetId, autoLaunch) { return json('/api/rdp/connect', 'POST', {targetId:targetId, autoLaunch:!!autoLaunch}); };
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
  window.goOpenConnections = async function () { window.open('/connections', '_blank', 'noopener'); return 'ok'; };
  window.goGetConnections = function () { return request('/api/connections'); };
  window.goClearConnections = function () { return request('/api/connections', {method:'DELETE'}); };
  window.goOpenConfigDir = async function () { var out = JSON.parse(await request('/api/config-path')); alert('配置文件：' + out.path); };
  window.goQuit = function () { return json('/api/quit', 'POST', {}); };
})();`
