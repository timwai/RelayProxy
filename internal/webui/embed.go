package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

// assets contains the shared Agent/Server design system. Keeping the visual
// primitives in one package prevents the two consoles from drifting apart.
//
//go:embed assets/base.css assets/theme.js
var assets embed.FS

// ReadAsset returns one shared UI asset for native WebView inlining.
func ReadAsset(name string) ([]byte, error) {
	return assets.ReadFile("assets/" + name)
}

// Handler serves the shared design-system assets to browser-hosted consoles.
func Handler() http.Handler {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}
