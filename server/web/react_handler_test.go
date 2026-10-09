package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReactConsoleFallbackOrBundle(t *testing.T) {
	handler := Handler()
	for _, route := range []string{"/", "/classic", "/css/style.css"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", route, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/classic", nil))
	if !strings.Contains(rec.Body.String(), `id="login-view"`) {
		t.Fatal("classic console must retain login UI")
	}
}

// React production builds must not hide the classic admin page or its assets.
func TestReactBundleRoutesAreIsolated(t *testing.T) {
	classic := fstest.MapFS{
		"index.html":    {Data: []byte(`<html><div id="login-view">Classic</div></html>`)},
		"css/style.css": {Data: []byte(`body{color:blue}`)},
	}
	react := fstest.MapFS{
		"index.html":     {Data: []byte(`<html><div id="root">React</div></html>`)},
		"assets/app.js":  {Data: []byte(`console.log('react')`)},
		"assets/app.css": {Data: []byte(`body{color:red}`)},
	}
	handler := newHandler(classic, react)
	for _, tc := range []struct{ path, expected string }{
		{"/", `id="root"`},
		{"/index.html", `id="root"`},
		{"/classic", `id="login-view"`},
		{"/classic/", `id="login-view"`},
		{"/css/style.css", `color:blue`},
		{"/server-assets/assets/app.js", `console.log('react')`},
		{"/server-assets/assets/app.css", `color:red`},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.expected) {
			t.Fatalf("%s: got HTTP %d: %q", tc.path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD /: got HTTP %d, body length %d", rec.Code, rec.Body.Len())
	}
}

func TestReactBundleMissingKeepsClassicIndex(t *testing.T) {
	classic := fstest.MapFS{"index.html": {Data: []byte(`<html>Classic fallback</html>`)}}
	handler := newHandler(classic, fstest.MapFS{"README.txt": {Data: []byte(`Unbuilt`)}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Classic fallback") {
		t.Fatalf("unbuilt React fallback: HTTP %d, %s", rec.Code, rec.Body.String())
	}
}
