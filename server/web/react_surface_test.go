package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerConsoleUsesReactVisualSurface(t *testing.T) {
	index, err := EmbeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `href="/css/react-surface.css"`) {
		t.Fatal("Server console does not load the React visual surface")
	}
	css, err := EmbeddedFiles.ReadFile("css/react-surface.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--rp-primary: #1677ff", "--rp-sidebar-width: 242px", "html[data-theme=\"dark\"]", "prefers-reduced-motion"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("React Server visual style missing %q", want)
		}
	}
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/css/react-surface.css", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "--rp-primary") {
		t.Fatalf("Server React visual stylesheet is not served: status %d", recorder.Code)
	}
}
