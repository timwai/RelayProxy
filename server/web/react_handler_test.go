package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestReactBundleRoutesAreIsolated(t *testing.T) {
	root := fstest.MapFS{"img/logo.svg": {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`)}}
	react := fstest.MapFS{
		"index.html":    {Data: []byte(`<html><div id="root">React</div></html>`)},
		"assets/app.js": {Data: []byte(`console.log('react')`)},
	}
	handler := newHandler(root, react)
	for _, tc := range []struct {
		path     string
		code     int
		expected string
	}{
		{"/", http.StatusOK, `id="root"`},
		{"/index.html", http.StatusOK, `id="root"`},
		{"/img/logo.svg", http.StatusOK, "<svg"},
		{"/server-assets/assets/app.js", http.StatusOK, "console.log"},
		{"/classic", http.StatusNotFound, ""},
		{"/classic/", http.StatusNotFound, ""},
		{"/js/app.js", http.StatusNotFound, ""},
		{"/css/style.css", http.StatusNotFound, ""},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.code || (tc.expected != "" && !strings.Contains(rec.Body.String(), tc.expected)) {
			t.Fatalf("%s: HTTP %d, body %q", tc.path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD /: HTTP %d, %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestReactBuildRequiredInsteadOfClassicFallback(t *testing.T) {
	handler := newHandler(fstest.MapFS{}, fstest.MapFS{"README.txt": {Data: []byte("not built")}})
	for _, path := range []string{"/", "/index.html"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "npm run build") {
			t.Fatalf("unbuilt React: %s HTTP %d: %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestEmbeddedVectorBrandAsset(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/img/logo.svg", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("SVG logo: HTTP %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("SVG logo: wrong content type %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "<svg ") || !strings.Contains(rec.Body.String(), "<path ") {
		t.Fatal("brand logo must contain scalable SVG paths")
	}
}
