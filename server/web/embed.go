package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// EmbeddedFiles includes the classic administration page and the optional
// React production assets. Unbuilt checkouts still serve the classic page.
//
//go:embed index.html js css img favicon.ico apple-touch-icon.png react_dist
var EmbeddedFiles embed.FS

// Handler serves the React console at / after Vite builds the bundle.
// /classic and all classic assets remain accessible for compatibility.
func Handler() http.Handler {
	root, err := fs.Sub(EmbeddedFiles, ".")
	if err != nil {
		panic(err)
	}
	react, err := fs.Sub(EmbeddedFiles, "react_dist")
	if err != nil {
		react = nil
	}
	return newHandler(root, react)
}

// newHandler is separated from the embedded file system so that the React
// bootstrap, asset routing and fallback can be covered by deterministic tests.
func newHandler(classicFS, reactFS fs.FS) http.Handler {
	classic := http.FileServer(http.FS(classicFS))
	reactReady := false
	if reactFS != nil {
		_, statErr := fs.Stat(reactFS, "index.html")
		reactReady = statErr == nil
	}
	var assets http.Handler
	if reactReady {
		assets = http.StripPrefix("/server-assets/", http.FileServer(http.FS(reactFS)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/classic" || r.URL.Path == "/classic/" {
			writeIndex(w, r, classicFS, "index.html", "classic console unavailable")
			return
		}
		if reactReady {
			if strings.HasPrefix(r.URL.Path, "/server-assets/") {
				assets.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				writeIndex(w, r, reactFS, "index.html", "React console unavailable")
				return
			}
		}
		classic.ServeHTTP(w, r)
	})
}

func writeIndex(w http.ResponseWriter, r *http.Request, source fs.FS, path, fallback string) {
	data, err := fs.ReadFile(source, path)
	if err != nil {
		http.Error(w, fallback, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
