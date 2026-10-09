package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// EmbeddedFiles includes both the classic console and the optional React bundle.
// An unbuilt checkout continues to serve the classic interface at /.
//
//go:embed index.html js css img favicon.ico apple-touch-icon.png react_dist
var EmbeddedFiles embed.FS

// Handler serves React at / after npm run build, with /classic always available.
// The existing administrative API and classic static assets remain unchanged.
func Handler() http.Handler {
	oldFS, err := fs.Sub(EmbeddedFiles, ".")
	if err != nil {
		panic(err)
	}
	classic := http.FileServer(http.FS(oldFS))
	reactFS, reactErr := fs.Sub(EmbeddedFiles, "react_dist")
	reactReady := false
	if reactErr == nil {
		_, statErr := fs.Stat(reactFS, "index.html")
		reactReady = statErr == nil
	}
	var assets http.Handler
	if reactReady {
		assets = http.StripPrefix("/server-assets/", http.FileServer(http.FS(reactFS)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/classic" || r.URL.Path == "/classic/" {
			data, readErr := fs.ReadFile(oldFS, "index.html")
			if readErr != nil {
				http.Error(w, "classic console unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(data)
			return
		}
		if reactReady {
			if strings.HasPrefix(r.URL.Path, "/server-assets/") {
				assets.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				data, readErr := fs.ReadFile(reactFS, "index.html")
				if readErr != nil {
					http.Error(w, "React console unavailable", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				_, _ = w.Write(data)
				return
			}
		}
		classic.ServeHTTP(w, r)
	})
}