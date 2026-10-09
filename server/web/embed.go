package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// EmbeddedFiles contains the React build output and static brand assets.
// Build server/web/frontend before packaging the production Server binary.
//
//go:embed img favicon.ico apple-touch-icon.png react_dist
var EmbeddedFiles embed.FS

// Handler serves only the React management console; the classic HTML/JS
// console and /classic route have been retired.
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

func newHandler(root, reactFS fs.FS) http.Handler {
	reactReady := false
	if reactFS != nil {
		_, err := fs.Stat(reactFS, "index.html")
		reactReady = err == nil
	}
	static := http.FileServer(http.FS(root))
	var assets http.Handler
	if reactReady {
		assets = http.StripPrefix("/server-assets/", http.FileServer(http.FS(reactFS)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			if !reactReady {
				http.Error(w, "RelayProxy Server React UI has not been built; run npm run build in server/web/frontend", http.StatusServiceUnavailable)
				return
			}
			writeIndex(w, r, reactFS, "index.html", "React console unavailable")
		case strings.HasPrefix(r.URL.Path, "/server-assets/"):
			if !reactReady {
				http.NotFound(w, r)
				return
			}
			assets.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/img/") ||
			r.URL.Path == "/favicon.ico" ||
			r.URL.Path == "/apple-touch-icon.png":
			static.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
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
