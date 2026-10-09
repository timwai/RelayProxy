package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyWebAssetsAreNotEmbedded(t *testing.T) {
	for _, path := range []string{"index.html", "js/app.js", "css/style.css", "css/react-surface.css"} {
		if _, err := fs.Stat(EmbeddedFiles, path); err == nil {
			t.Errorf("obsolete classic console asset still embedded: %s", path)
		}
	}
	for _, path := range []string{"/classic", "/js/app.js", "/css/style.css"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("retired route %s: HTTP %d", path, rec.Code)
		}
	}
}
