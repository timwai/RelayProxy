package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// With a Vite build present, browser clients and macOS WKWebView must load
// exactly the same React entrypoint as Wails, but use the HTTP bridge instead.
func TestReactWebEntrypointAndAssets(t *testing.T) {
	if !hasBuiltReactWebUI() {
		t.Skip("Vite bundle not built; Go-only build uses legacy UI")
	}
	_, handler := webTestHandler(newWebTestBridge(t), true)
	index, err := assets.ReadFile("react_dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	if home.Code != http.StatusOK {
		t.Fatalf("React index status: %d", home.Code)
	}
	if !strings.Contains(home.Body.String(), `<script src="/web-bridge.js"></script>`) {
		t.Fatal("HTTP bridge is not installed before React entrypoint")
	}
	if !strings.Contains(home.Body.String(), `id="root"`) {
		t.Fatal("React root missing")
	}
	re := regexp.MustCompile(`(?:src|href)="/(assets/[^"]+\.(?:js|css))"`)
	matches := re.FindAllStringSubmatch(string(index), -1)
	if len(matches) < 2 {
		t.Fatalf("Vite entrypoint lacks JS/CSS assets: %s", index)
	}
	for _, match := range matches {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/"+match[1], nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.Len() == 0 {
			t.Errorf("embedded asset %s: HTTP %d (%d bytes)", match[1], response.Code, response.Body.Len())
		}
	}
	for _, path := range []string{"/assets/README.txt", "/assets/nonexistent.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("asset path %q status = %d, want 404", path, response.Code)
		}
	}
	traversal := httptest.NewRecorder()
	serveReactAsset(traversal, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/assets/../../web.go", nil))
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("asset traversal status = %d", traversal.Code)
	}
}

func TestReactWebConfigAndPlatformBridge(t *testing.T) {
	_, handler := webTestHandler(newWebTestBridge(t), true)
	configResponse := httptest.NewRecorder()
	handler.ServeHTTP(configResponse, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/config", nil))
	if configResponse.Code != http.StatusOK {
		t.Fatalf("config status: %d", configResponse.Code)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(configResponse.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"revision", "serverAddress", "insecureTls", "publicDirectAdvertise", "networkCapabilities", "p2p", "routing"} {
		if _, ok := config[key]; !ok {
			t.Errorf("React config missing %s", key)
		}
	}
	var p2p map[string]json.RawMessage
	if err := json.Unmarshal(config["p2p"], &p2p); err != nil {
		t.Fatal(err)
	}
	if _, ok := p2p["upnpAllowed"]; !ok {
		t.Fatal("React P2P config missing UPnP flag")
	}
	capabilities := httptest.NewRecorder()
	handler.ServeHTTP(capabilities, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/network-capabilities", nil))
	if capabilities.Code != http.StatusOK || !json.Valid(capabilities.Body.Bytes()) {
		t.Fatalf("network-capabilities: HTTP %d %q", capabilities.Code, capabilities.Body.String())
	}
	js := httptest.NewRecorder()
	handler.ServeHTTP(js, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/web-bridge.js", nil))
	for _, want := range []string{"goGetNetworkCapabilities", "goRestart", "relayproxyLifecycle"} {
		if !strings.Contains(js.Body.String(), want) {
			t.Errorf("HTTP React bridge missing %s", want)
		}
	}
}
