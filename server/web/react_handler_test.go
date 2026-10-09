package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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