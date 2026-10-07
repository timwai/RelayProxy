//go:build nativecore

package gui

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeCoreOmitsBrowserRoutes(t *testing.T) {
	server := &WebServer{}
	mux := http.NewServeMux()
	server.registerBrowserRoutes(mux)

	for _, path := range []string{"/", "/connections", "/web-bridge.js", "/ui/base.css"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}
}
