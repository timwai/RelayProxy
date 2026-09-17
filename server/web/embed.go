package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed index.html js css img favicon.ico apple-touch-icon.png
var EmbeddedFiles embed.FS

// Handler returns an http.Handler serving the embedded web UI
func Handler() http.Handler {
	fsys, err := fs.Sub(EmbeddedFiles, ".")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(fsys))
}
