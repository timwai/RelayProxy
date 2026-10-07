//go:build nativecore

package gui

import "net/http"

// The embedded WinUI Core exposes only the authenticated loopback /api/* surface.
// Browser HTML/JS/CSS assets are deliberately not linked into this build.
func (w *WebServer) registerBrowserRoutes(_ *http.ServeMux) {}
