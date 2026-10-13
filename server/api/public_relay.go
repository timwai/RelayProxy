package api

import "net/http"

// PublicRelayHandler is the only HTTP surface on the shared Agent tunnel
// port. It must never route an arbitrary URL to the Admin router: Browser
// device identity is independent of Server administration permissions.
func PublicRelayHandler(push, browser, proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case req.Method == http.MethodConnect:
			if proxy == nil {
				http.Error(w, "browser network proxy disabled", http.StatusForbidden)
				return
			}
			proxy.ServeHTTP(w, req)
		// A status-only, non-credentialed check allows Chrome to distinguish
		// a missing/outdated TLS relay endpoint from a refused WebSocket
		// Upgrade without exposing Admin routes or enrolled device data.
		case req.Method == http.MethodGet && req.URL.Path == "/api/v1/browser-sync/health":
			if browser == nil {
				http.NotFound(w, req)
			} else if req.TLS == nil {
				http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		case req.URL.Path == "/api/v1/browser-sync/extensions/register",
			req.URL.Path == "/api/v1/browser-sync/devices/register",
			req.URL.Path == "/api/v1/browser-sync/ws":
			if browser == nil {
				http.NotFound(w, req)
				return
			}
			browser.ServeHTTP(w, req)
		case req.URL.Path == "/api/v1/push" || len(req.URL.Path) > 13 &&
			req.URL.Path[:13] == "/api/v1/push/":
			if push == nil {
				http.NotFound(w, req)
				return
			}
			push.ServeHTTP(w, req)
		default:
			http.NotFound(w, req)
		}
	})
}
